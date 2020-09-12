## Install

```bash
go get github.com/gagliardetto/lgtm-cli

export LGTM_CLI_CONFIG=/path/to/credentials.json # see example_credentials.json

make
```

### unfollow all followed projects

```bash
lgtm unfollow-all
```

### list all followed projects

```bash
lgtm followed
```

### follow one or more projects

```bash
lgtm follow github/codeql-go kubernetes/kubernetes
```

### follow one or more projects from file

```bash
lgtm follow \
	-f=projects.txt
```

### follow all projects of a specific owner

```bash
lgtm follow github
```

### list all lists

```bash
lgtm lists
```

### create a new list

```bash
lgtm create-list "name_of_list"
```

### list projects in a list

```bash
lgtm list name_of_list
```

### add one or more projects to a list

```bash
lgtm add-to-list \
	github/codeql-go kubernetes/kubernetes \
	--name="name_of_list"
```

### add projects to a list from a file

```bash
lgtm add-to-list \
	--name="name_of_list" \
	-f=projects.txt
```

### delete a list (NOTE: projects will NOT be unfollowed if they are followed)

```bash
lgtm delete-list "test-list"
```

### unfollow one or more projects

```bash
lgtm unfollow github/codeql-go kubernetes/kubernetes
```

### unfollow a list of projects from file

```bash
lgtm unfollow \
	-f=projects.txt
```

### unfollow all projects from a certain owner (e.g. all projects from kubernetes owner)

```bash
lgtm unfollow kubernetes
```

### rebuild followed projects for a specific language (default: rebuild ONLY projects that don't have a build for that language, yet)

```bash
lgtm --wait=30s rebuild --lang=go
```

### run a query on a specific "project list"

```bash
lgtm query \
	--list-key=0123456789 \
	-lang=go \
	-q=/path/to/query.ql
```

### run a query on one or more projects

```bash
lgtm query \
	github/codeql-go kubernetes/kubernetes \
	-lang=go \
	-q=/path/to/query.ql
```

### run a query on projects from a file

```bash
lgtm query \
	-lang=go \
	-f=projects.txt \
	-q=/path/to/query.ql
```

## Legal

The author of this script assumes no liability for your use of this project, including, but not limited legal repercussions or being banned from LGTM.com. Please consult the LGTM.com terms of service for more information.

## Credits

`Legal` section of this readme: https://github.com/JLLeitschuh/lgtm_hack_scripts#legal