## Install

```bash
go get -u github.com/gagliardetto/lgtm-cli

make install

export LGTM_CLI_CONFIG=/path/to/lgtm.com_credentials.json # see example below
```

or

```bash
cd $(mktemp -d)

git clone https://github.com/gagliardetto/lgtm-cli.git

cd lgtm-cli

make install

export LGTM_CLI_CONFIG=/path/to/lgtm.com_credentials.json # see example below
```

## Example `lgtm.com_credentials.json`

```json
{
  "api_version": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "session": {
    "nonce": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "long_session": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "short_session": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  },
  "github": {
    "token": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }
}
```

You can intercept the lgtm.com session values from Chrome WebDev tools (and similar) after you've logged into lgtm.com.

As for the GitHub token, one with **zero** permissions is advised.

---

## LGTM-CLI usage

### Unfollow all followed projects

```bash
lgtm unfollow-all
```

### List all followed projects

```bash
lgtm followed
```

### Follow one or more projects

```bash
lgtm follow github/codeql-go kubernetes/kubernetes
```

### Follow one or more projects from file

```bash
lgtm follow \
	-f=projects.txt
```

### Follow all projects of a specific owner

```bash
lgtm follow github
```

### Follow all projects of a specific language (experimental)

```bash
lgtm follow-by-lang --limit=101 python
```

### Follow all projects from a specific search query on repository metadata

Results are limited (by the GitHub API) to the first 1K items.

Follow GitHub repositories that match your provided **repository search query**.

For query syntax, see : https://docs.github.com/en/free-pro-team@latest/github/searching-for-information-on-github/searching-for-repositories

**NOTE:** lgtm.com does not support fork scanning, so to get more relevant repositories, it's always advised to include `fork:false` in your search query.

```bash
lgtm follow-by-meta-search --limit=101 'jquery "hello world" in:name,description language:javascript fork:false'
```

### Follow all projects from a specific code search query

Results are limited (by the GitHub API) to the first 1K items.

Follow GitHub repositories that match your provided **code search query**.

For query syntax, see: https://docs.github.com/en/free-pro-team@latest/github/searching-for-information-on-github/searching-code

```bash
lgtm follow-by-code-search --limit=101 'from flask import Flask language:python filename:"__init__.py"'
```

### List all lists

```bash
lgtm lists
```

### Create a new list

```bash
lgtm create-list "name_of_list"
```

### List projects in a list

```bash
lgtm list "name_of_list"
```

### Add one or more projects to a list

```bash
lgtm add-to-list \
	github/codeql-go kubernetes/kubernetes \
	--name="name_of_list"
```

### Add projects to a list from a file

```bash
lgtm add-to-list \
	--name="name_of_list" \
	-f=projects.txt
```

### Delete a list

```bash
lgtm delete-list "name_of_list"
```

**NOTE**: projects will NOT be unfollowed if they are followed.

### Unfollow one or more projects

Supports glob matching.

```bash
lgtm unfollow github/codeql-go "kubernetes/*" "foo/b*" "*/hello"
```

### Unfollow a list of projects from file

```bash
lgtm unfollow \
	-f=projects.txt
```

### Unfollow all projects from a certain owner

Example: unfollow all projects from kubernetes owner.

```bash
lgtm unfollow kubernetes
```

### Rebuild followed projects for a specific language

```bash
lgtm --wait=30s rebuild --lang=go
```

Default: rebuild ONLY projects that don't have a build for that language, yet.

### Trigger a build attempt for proto-projects

```bash
lgtm --wait=5s rebuild-proto
```

or to not be prompted for confirmation for each item:

```bash
lgtm --wait=5s rebuild-proto --force
```


### Run a query on a specific "project list"

```bash
lgtm query \
	--list-key=0123456789 \
	-lang=go \
	-q=/path/to/query.ql
```

### Run a query on one or more projects

```bash
lgtm query \
	github/codeql-go kubernetes/kubernetes \
	-lang=go \
	-q=/path/to/query.ql
```

### Run a query on projects from a file

```bash
lgtm query \
	-lang=go \
	-f=projects.txt \
	-q=/path/to/query.ql
```

---

## Legal

The author of this script assumes no liability for your use of this project, including, but not limited legal repercussions or being banned from LGTM.com. Please consult the LGTM.com terms of service for more information.

## Credits

`Legal` section of this readme: https://github.com/JLLeitschuh/lgtm_hack_scripts#legal
