## setup

```
export LGTM_CLI_CONFIG=credentials.json
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
