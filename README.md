### compose-hook

git pre-receive/post-receive docker-compose hook.

### Install

    go get -u github.com/wader/compose-hook

### Usage:

Use `compose-hook` directly as a pre-receive/post-receive symlink or call it from hook script with `old` `new` `ref` as arguments.

Add a `compose-hook.yml` file to your git repo.

### Configuration

```yaml
master: # branch name
  project: webapp # required
  file: docker-compose_dev.yml # optionl
  skip_pull: false # optional
  skip_build: false # optional
  skip_up: false # optional
  tail_log: 5s # optional, duration to tail conatiner logs after up
  smart_recreate: false # optional, use up --x-smart-recreate

testing: # some other branch name
  # ...

```

### Notes and TODOs

To use in pre-receive/post-receive hook script:

```sh
while read old new ref ; do
  # do other stuff etc
  compose-hook $old $new $ref
done
```

Currenrly `tail_log` does not work because of https://github.com/docker/compose/issues/1838

Use `--config` to use another filenamn than `compose-hook.yml`.

I use compose-hook in combination with [nginx-proxy](https://github.com/jwilder/nginx-proxy).

Run tests: `go build && test/run $PWD/compose-hook $PWD/test`

Add config to cleanup images and containers.

Wildcard branch name https://github.com/docker/compose/pull/1765. Export branch name?

Option to stop on remove branch? look for config in old commit.

### License

compose-hook is licensed under the MIT license. See [LICENSE](LICENSE) for the full license text.
