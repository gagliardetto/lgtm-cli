## Install

```bash
(cd && GO111MODULE=on go get github.com/gagliardetto/lgtm-cli)

cd $GOPATH/src/github.com/gagliardetto/lgtm-cli

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

You can intercept the lgtm.com session values from Chrome WebDev tools (and similar) after you've logged into lgtm.com (see below for tutorial).

As for the GitHub token, one with **zero** permissions is advised (i.e. all scope checkboxes **non-selected**). You can create a new token here: https://github.com/settings/tokens/new

## [Chrome] Where to find the lgtm.com API credentials

1. Got to https://lgtm.com/ and signup/login.
2. After you're logged in, open the Chrome DevTools > `Network` tab:

	![2021-01-22_19-08](https://user-images.githubusercontent.com/15271561/105528478-b34b6780-5ced-11eb-8a59-aa72767121c8.png)
3. Reload lgtm.com.
4. Set filter to `XHR`:

	![2021-01-22_18-52](https://user-images.githubusercontent.com/15271561/105526676-65356480-5ceb-11eb-9257-3fddaac6ce91.png)
5. Click on an API request:

	![2021-01-22_18-33](https://user-images.githubusercontent.com/15271561/105526873-a6c60f80-5ceb-11eb-95ce-de800a76c2a7.png)
6. Go to the `Headers` tab and scroll to the `Request Headers` section:

	![2021-01-22_18-56](https://user-images.githubusercontent.com/15271561/105527064-e55bca00-5ceb-11eb-8c48-1a8970d8f5a0.png)
7. Copy the values into your `lgtm.com_credentials.json` file:
	- You can find the `api_version` in highlighted point `2` in the above image.
	- You can find the `session.nonce` in highlighted point `5` in the above image.
	- You can find the `session.long_session` in highlighted point `4` in the above image.
	- You can find the `session.short_session` in highlighted point `3` in the above image.

**NOTES**

- The `api_version` is a value that changes from time to time.
- If you get a `... resp.error="bad request"; resp.message="api_mismatch"`, it means it's time to go look for the new `api_version` value from lgtm.com.

---

## LGTM-CLI usage

For the complete docs about all the commands: `lgtm help`; or for a specific command: `lgtm help <command>`

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

By list **name** (can specify multiple):

```bash
lgtm query \
	--list="foo" \
	--list="bar" \
	-lang=go \
	-q=/path/to/query.ql
```

or by list **key** (can specify multiple):

```bash
lgtm query \
	--list-key=0123456789 \
	--list-key=0987654321 \
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

## Experimental commands

### Get results from a query ID

```bash
lgtm x-list-query-results XXXXXXXXXXXXXXXXXXX
```

#### Examples

##### Get projects name
```bash
lgtm x-list-query-results XXXXXXXXXXXXXXXXXXX | jq -r '.[].Project.displayName'
```

##### List project URLs of projects that have at least one result in the query run

```bash
lgtm x-list-query-results XXXXXXXXXXXXXXXXXXX --min-results=1 |  jq -r ".[].Project.externalURL.url"
```

##### List project URLs of projects that have at least one alert in the query run

```bash
lgtm x-list-query-results XXXXXXXXXXXXXXXXXXX --min-alerts=1 |  jq -r ".[].Project.externalURL.url"
```

---

## Known errors

### Cannot get list of followed projects

Multiple commands do some checks and optimizzations that rely on getting the list of followed projects.

When you follow many projects (a few thousands, probably about 5K or more), the lgtm.com API endpoint that lists followed projects does timeout.

To overcome that, you can use the `--ignore-followed-errors` flag to use alternative methods to complete the command.

Example:

```bash
lgtm --ignore-followed-errors unfollow kubernetes/kubernetes
```

This of course won't work for commands like `lgtm followed` or `lgtm unfollow-all`.

---

## Legal

The author and contributors of this script assume no liability for your use of this project, including, but not limited legal repercussions or being banned from LGTM.com. Please consult the LGTM.com terms of service for more information.

LGTM/LGTM.com is a trademark of Semmle / GitHub. The use of the LGTM trademark and name on this page shall not imply any affiliation with or endorsement by Semmle / GitHub.

## Credits

`Legal` section of this readme: https://github.com/JLLeitschuh/lgtm_hack_scripts#legal
