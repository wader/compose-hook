package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v2"
)

const deleteBranchHash = "0000000000000000000000000000000000000000"

var composeHookFile = flag.String("config", "compose-hook.yml", "compose-hook config filename")

type branchConfigs map[string]branchConfig
type branchConfig struct {
	File          string
	Project       string
	SkipPull      bool          `yaml:"skip_pull"`
	SkipBuild     bool          `yaml:"skip_build"`
	SkipUp        bool          `yaml:"skip_up"`
	Taillog       time.Duration `yaml:"tail_log"`
	SmartRecreate bool          `yaml:"smart_recreate"`
}

func logf(format string, a ...interface{}) {
	fmt.Printf("compose-hook: "+format+"\n", a...)
}

func newBranchConfigsFromFile(path string) (branchConfigs, error) {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	bc := branchConfigs{}
	if err := yaml.Unmarshal(bytes, &bc); err != nil {
		return nil, err
	}

	for branchName, config := range bc {
		if config.Project == "" {
			return nil, fmt.Errorf("%s: no project name", branchName)
		}
	}

	return bc, nil
}

type preReceive struct {
	oldHash string
	newHash string
	refName string
}

func newPreReceiveFromLine(line string) (*preReceive, error) {
	parts := strings.Split(line, " ")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid pre-receive line")
	}

	return &preReceive{
		oldHash: parts[0],
		newHash: parts[1],
		refName: parts[2],
	}, nil
}

func (pr preReceive) refType() string {
	// revName looks like "refs/<type>/test"
	parts := strings.SplitN(pr.refName, "/", 3)
	// ["refs" "<type>" "<..>"]
	return parts[1]
}

func (pr preReceive) branchName() string {
	// revName looks like "refs/heads/<branch>"
	parts := strings.SplitN(pr.refName, "/", 3)
	// ["refs" "heads" "<branch>""]
	return parts[2]
}

type composeHook struct {
	tempDir string
	gitDir  string
}

func runCmd(cmd *exec.Cmd, timeout time.Duration) error {
	logf("%s", strings.Join(cmd.Args, " "))

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	if timeout > 0 {
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()
		select {
		case <-time.After(timeout):
			// kill seems to be a bad idea as docker-compose has child processes
			cmd.Process.Signal(syscall.SIGTERM)
			<-done
		case err := <-done:
			if err != nil {
				return err
			}
		}
	} else {
		if err := cmd.Wait(); err != nil {
			return err
		}
	}

	return nil
}

func (ch *composeHook) outputGitRev(hash string) (string, error) {
	hashPath := path.Join(ch.tempDir, hash)

	if err := os.Mkdir(hashPath, 0700); err != nil {
		if os.IsExist(err) {
			return hashPath, nil
		}
		return "", err
	}

	commands := [][]string{
		{"git", "clone", "--quiet", ch.gitDir, "."},
		// --git-dir=.git to override GIT_DIR=. env that git hooks run with
		{"git", "--git-dir=.git", "checkout", "--quiet", hash},
		{"git", "--git-dir=.git", "submodule", "--quiet", "update", "--init", "--recursive"},
	}

	for _, command := range commands {
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Dir = hashPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return "", err
		}
		if err := cmd.Wait(); err != nil {
			return "", err
		}
	}

	// normalize file times
	// workaround for https://github.com/docker/docker/pull/12031 fixed in 1.8
	zeroTime := time.Unix(0, 0)
	filepath.Walk(
		hashPath,
		func(path string, info os.FileInfo, err error) error {
			os.Chtimes(path, zeroTime, zeroTime)
			return nil
		})

	return hashPath, nil
}

func (ch *composeHook) processPreReceiveWithConfig(hashPath string, pr *preReceive, config *branchConfig) error {
	var composeEnv []string
	if config.File != "" {
		composeEnv = append(composeEnv, fmt.Sprintf("COMPOSE_FILE=%s", config.File))
	}
	if config.Project != "" {
		composeEnv = append(composeEnv, fmt.Sprintf("COMPOSE_PROJECT_NAME=%s", config.Project))
	}
	logf("%s %s", pr.branchName(), strings.Join(composeEnv, " "))

	env := append(os.Environ(), composeEnv...)

	// pull updated images
	if !config.SkipPull {
		c := exec.Command("docker-compose", "pull")
		c.Dir = hashPath
		c.Env = env
		if err := runCmd(c, 0); err != nil {
			return err
		}
	}

	// (re)build local images if needed
	if !config.SkipBuild {
		c := exec.Command("docker-compose", "build")
		c.Dir = hashPath
		c.Env = env
		if err := runCmd(c, 0); err != nil {
			return err
		}
	}

	// re(create) containers and (re)start them if needed
	// smart recreate: https://github.com/docker/compose/pull/1399
	if !config.SkipUp {
		cmds := []string{"docker-compose", "up", "-d"}
		if config.SmartRecreate {
			cmds = append(cmds, "--x-smart-recreate")
		}
		c := exec.Command(cmds[0], cmds[1:]...)
		c.Dir = hashPath
		c.Env = env
		if err := runCmd(c, 0); err != nil {
			return err
		}
	}

	if config.Taillog > 0 {
		c := exec.Command("docker-compose", "logs", "--no-color")
		c.Dir = hashPath
		// workaround for https://github.com/docker/compose/issues/1838
		c.Env = append(env, "PYTHONUNBUFFERED=1")
		if err := runCmd(c, config.Taillog); err != nil {
			return err
		}
	}

	return nil
}

func (ch *composeHook) processPreReceive(pr *preReceive) error {
	// ignore non-branch (tags) and deleted branch
	if pr.refType() != "heads" ||
		pr.newHash == deleteBranchHash {
		return nil
	}

	hashPath, err := ch.outputGitRev(pr.newHash)
	if err != nil {
		return err
	}

	composeHookPath := path.Join(hashPath, *composeHookFile)
	if _, err := os.Stat(composeHookPath); err != nil {
		logf("%s: %s not found", pr.branchName(), *composeHookFile)
		return nil
	}

	configs, err := newBranchConfigsFromFile(composeHookPath)
	if err != nil {
		return err
	}

	for branchName, config := range configs {
		if branchName != pr.branchName() {
			continue
		}

		if err := ch.processPreReceiveWithConfig(hashPath, pr, &config); err != nil {
			return err
		}
	}

	return nil
}

func run() error {
	flag.Parse()

	gitDir, err := os.Getwd()
	if err != nil {
		return err
	}

	tempDir, err := ioutil.TempDir("", "compose-hook")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	ch := composeHook{
		tempDir: tempDir,
		gitDir:  gitDir,
	}

	if flag.NArg() == 3 {
		// from args
		if err := ch.processPreReceive(&preReceive{
			oldHash: flag.Arg(0),
			newHash: flag.Arg(1),
			refName: flag.Arg(2),
		}); err != nil {
			return err
		}
	} else if flag.NArg() == 0 {
		// from stdin
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()

			pr, err := newPreReceiveFromLine(line)
			if err != nil {
				return err
			}
			if err := ch.processPreReceive(pr); err != nil {
				return err
			}
		}
	} else {
		return fmt.Errorf("please run no args for stdin or args old new ref")
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		logf("%v", err)
		os.Exit(1)
	}
}
