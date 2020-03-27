## setup

```
export LGTM_CLI_CONFIG=$GOPATH/src/github.com/gagliardetto/lgtm-cli/credentials.json
```

## golang: get all dependencies of all (sub)packages of repo

```
cd $GOPATH/src/github.com/kubernetes/kubernetes

for dir in $(find . -type d ! -path "*/vendor/*"| sort -u); do
	(cd "$dir"; deplist)
done | sort -u >| all_dependencies.txt
```

## filter

```
cat $GOPATH/src/github.com/gagliardetto/lgtm-cli/kube_reduced.txt | cut -f1,2,3 -d'/' | sort -u
```

## high level actions

- unfollow all projects
- follow a project: from input, or from file
- unfollow a project: from autocomplete, or from file
- add a project to a list: from autocomplete, or from file
- remove a project from a list

## low-lever actions

- get list of followed projects
- unfollow all
- get followed, check if item matches, and add to sublist
- get list of lists


# unfollow all projects
lgtm unfollow-all

# unfollow one or more projects
lgtm unfollow kubernetes github/codeql-go \
	-f=projects.txt

# follow one or more projects
lgtm follow kubernetes github/codeql-go \
	-f=projects.txt

# run a query on multiple projects; -F means follow what is not followed
lgtm query \
	kubernetes \
	github/codeql-go \
	-lang=go \
	-f=projects-a.txt \
	-f=projects-b.txt \
	-q=/path/to/query-0.ql \
	-F

# run one or more queries on all projects
lgtm query-all \
	-lang=go \
	-q=/path/to/query-0.ql \
	-q=/path/to/query-1.ql

---

# -F means create list if not exists, follow projects if not followed
lgtm list-add "test-list" kubernetes github/codeql-go \
	-f=projects.txt \
	-F

# delete a list (no project will be unfollowed)
lgtm delete-list "test-list"