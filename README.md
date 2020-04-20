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
- unfollow specific projects
- follow a project: from input, or from file
- run query on projects

# unfollow all projects
lgtm unfollow-all

# unfollow one or more projects
lgtm unfollow kubernetes github/codeql-go \
	-f=projects.txt

# follow one or more projects
lgtm follow kubernetes github/codeql-go \
	-f=projects.txt

# run a query on multiple projects
lgtm query \
	kubernetes \
	github/codeql-go \
	-lang=go \
	-f=projects-a.txt \
	-f=projects-b.txt \
	-q=/path/to/query-0.ql \
	-F

# run a query on one or more project lists:
lgtm query \
	--list-key=1234567890 \
	-lang=go \
	-q=./hello-world.ql

---

# list all followed projects:
lgtm followed

# list all lists:
lgtm lists

# create a new list:
lgtm create-list name_of_list

# list projects in a list:
lgtm list name_of_list

# add projects to a list:
lgtm add-to-list --name="new_list" kubernetes \
	-f=projects.txt

# delete a list (no project will be unfollowed)
lgtm delete-list "test-list"