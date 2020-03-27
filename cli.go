package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/c-bata/go-prompt"
	ghc "github.com/d1ss0lv3/ciccio/gh-client"
	"github.com/gagliardetto/bianconiglio"
	"github.com/gagliardetto/request"
	. "github.com/gagliardetto/utils"
	"github.com/google/go-github/github"
	"github.com/goware/urlx"
	"github.com/urfave/cli"
	"go.uber.org/ratelimit"
)

const (
	githubHost  = "https://github.com"
	defaultHost = githubHost
)

var (
	apiRateLimiter = ratelimit.New(1, ratelimit.WithoutSlack)
	ghClient       *ghc.Client
)

func main() {

	var configFilepath string
	var client *Client

	///////////////////////////////////////////////////////////////////////////////////////////////////////////////
	unfollower := func(isProto bool, key string, name string, done int64, tot int64) {
		Infof(
			"Unfollowing %s; progress is %s (%v/%v)",
			name,
			GetFormattedPercent(done, tot),
			done,
			tot,
		)

		unfollowFunc := client.UnfollowProject
		if isProto {
			unfollowFunc = client.UnfollowProtoProject
		}

		err := unfollowFunc(key)
		if err != nil {
			Errorf(
				"error while unfollowing project %s: %s",
				name,
				err,
			)
		} else {
			Successf(
				"Successfully unfollowed %s; progress is %s (%v/%v)",
				name,
				GetFormattedPercent(done, tot),
				done,
				tot,
			)
		}
	}

	follower := func(u string, done int64, tot int64) {
		Infof(
			"Following %s; progress is %s (%v/%v)",
			u,
			GetFormattedPercent(done, tot),
			done,
			tot,
		)

		err := client.FollowProject(u)
		if err != nil {
			Errorf(
				"error while following project %s: %s",
				u,
				err,
			)
		} else {
			Successf(
				"Successfully followed %s; progress is %s (%v/%v)",
				u,
				GetFormattedPercent(done, tot),
				done,
				tot,
			)
		}
	}

	protoRebuilder := func(key string, name string) {
		Infof(
			"Forcing rebuild of proto project %s",
			name,
		)

		err := client.RebuildProtoProject(key)
		if err != nil {
			Errorf(
				"error while forcing rebuild of proto project %s: %s",
				name,
				err,
			)
		} else {
			Successf(
				"Successfully forced rebuild of proto project %s",
				name,
			)
		}
	}
	///////////////////////////////////////////////////////////////////////////////////////////////////////////////
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "conf",
				Usage:       "path to credentials.json file",
				Destination: &configFilepath,
			},
		},
		Before: func(c *cli.Context) error {

			configFilepathFromEnv := os.Getenv("LGTM_CLI_CONFIG")

			if configFilepath == "" && configFilepathFromEnv == "" {
				return errors.New(c.App.Usage)
			}

			// if the flag is not set, use env variable:
			if configFilepath == "" {
				configFilepath = configFilepathFromEnv
			}

			conf, err := LoadConfigFromFile(configFilepath)
			if err != nil {
				panic(err)
			}

			client, err = NewClient(conf)
			if err != nil {
				panic(err)
			}

			// setup github client:
			ghClient = ghc.NewClient(conf.GitHub.Token)

			ghc.ResponseCallback = func(resp *github.Response) {
				if resp == nil {
					return
				}
				if resp.Rate.Remaining < 1000 {
					Debugf("{%v/%v}", resp.Rate.Remaining, resp.Rate.Limit)
				}
			}
			//IsExitingFunc = IsExiting
			return nil
		},
		Commands: []cli.Command{
			{
				Name:  "unfollow-all",
				Usage: "Unfollow all currently followed repositories (\"projects\".",
				Action: func(c *cli.Context) error {
					// TODO:
					// - get list of all followed
					// - unfollow each

					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					totalProjects := len(projects)
					totalProtoProjects := len(protoProjects)
					total := totalProjects + totalProtoProjects

					Infof("You are following %v projects", total)

					if total == 0 {
						return nil
					}
					Infof("Starting to unfollow all...")

					for index, pr := range projects {
						unfollower(false, pr.Key, pr.ExternalURL.URL, int64(index+1), int64(total))
					}
					for index, proto := range protoProjects {
						unfollower(true, proto.Key, proto.CloneURL, int64(index+1+totalProjects), int64(total))
					}
					return nil
				},
			},
			{
				Name:  "unfollow",
				Usage: "Unfollow one or more projects.",
				Action: func(c *cli.Context) error {
					repoURLsRaw := []string(c.Args())
					hasRepoList := c.IsSet("f")
					if hasRepoList {
						repoListFilepaths := c.StringSlice("f")
						for _, path := range repoListFilepaths {
							err := ReadConfigLinesAsString(path, func(line string) bool {
								repoURLsRaw = append(repoURLsRaw, line)
								return true
							})
							if err != nil {
								return err
							}
						}
					}
					repoURLsRaw = Deduplicate(repoURLsRaw)

					repoURLs := make([]string, 0)
					for _, raw := range repoURLsRaw {
						owner, isWholeUser, err := IsUserOnly(raw)
						if err != nil {
							panic(err)
						}
						if isWholeUser {
							Debugf("Getting list of repos for %s ...", owner)
							repos, err := GithubGetRepoList(owner)
							if err != nil {
								panic(fmt.Errorf("error while getting repo list for user %q: %s", owner, err))
							}
							Debugf("%s has %v repos", owner, len(repos))
							for _, repo := range repos {
								//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
								repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
							}
						} else {
							parsed, err := ParseGitURL(raw, false)
							if err != nil {
								panic(err)
							}
							repoURLs = append(repoURLs, parsed.URL())
						}
					}

					Infof("Getting list of followed projects...")
					projects, _, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}

					toBeUnfollowed := make([]*Project, 0)
					// match repos against list of repos followed:
					for _, pr := range projects {
						isToBeUnfollowed := SliceContains(repoURLs, pr.ExternalURL.URL)
						if isToBeUnfollowed {
							toBeUnfollowed = append(toBeUnfollowed, pr)
						}
					}
					Infof("Will unfollow %v projects...", len(toBeUnfollowed))
					// unfollow projects:
					for index, pr := range toBeUnfollowed {
						unfollower(false, pr.Key, pr.ExternalURL.URL, int64(index+1), int64(len(repoURLs)))
					}
					return nil
				},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
				},
			},
			{
				Name:  "follow",
				Usage: "Follow one or more projects.",
				Action: func(c *cli.Context) error {
					repoURLsRaw := []string(c.Args())
					hasRepoList := c.IsSet("f")
					if hasRepoList {
						repoListFilepaths := c.StringSlice("f")
						for _, path := range repoListFilepaths {
							err := ReadConfigLinesAsString(path, func(line string) bool {
								repoURLsRaw = append(repoURLsRaw, line)
								return true
							})
							if err != nil {
								return err
							}
						}
					}
					repoURLsRaw = Deduplicate(repoURLsRaw)

					repoURLs := make([]string, 0)
					for _, raw := range repoURLsRaw {
						owner, isWholeUser, err := IsUserOnly(raw)
						if err != nil {
							panic(err)
						}
						if isWholeUser {
							Debugf("Getting list of repos for %s ...", owner)
							repos, err := GithubGetRepoList(owner)
							if err != nil {
								panic(fmt.Errorf("error while getting repo list for user %q: %s", owner, err))
							}
							Debugf("%s has %v repos", owner, len(repos))
							for _, repo := range repos {
								//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
								isFork := repo.GetFork()
								// "Currently we do not support analysis of forks. Consider adding the parent of the fork instead."
								if !isFork {
									repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
								} else {
									Warnf("Skipping fork %s", repo.GetFullName())
								}
							}
						} else {
							parsed, err := ParseGitURL(raw, false)
							if err != nil {
								panic(err)
							}
							repoURLs = append(repoURLs, parsed.URL())
						}
					}

					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}

					alreadyFollowed := func(projectURL string) bool {
						for _, pr := range projects {
							alreadyFollowed := projectURL == pr.ExternalURL.URL
							if alreadyFollowed {
								return true
							}
						}
						return false
					}
					IsProto := func(projectURL string) (*ProtoProject, bool) {
						for _, pr := range protoProjects {
							found := projectURL == pr.CloneURL
							if found {
								return pr, true
							}
						}
						return nil, false
					}
					toBeFollowed := make([]string, 0)
					// exclude already-followed projects:
					for _, repoURL := range repoURLs {
						if !alreadyFollowed(repoURL) {
							toBeFollowed = append(toBeFollowed, repoURL)
						}
					}
					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)
					// follow repos:
					for index, repoURL := range toBeFollowed {
						forceRebuild := c.Bool("F")
						proto, isProto := IsProto(repoURL)
						if isProto && forceRebuild {
							protoRebuilder(proto.Key, repoURL)
						} else {
							follower(repoURL, int64(index+1), int64(totalToBeFollowed))
						}
					}
					return nil
				},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
					&cli.BoolFlag{
						Name:  "force, F",
						Usage: "Force re-building proto-projects.",
					},
				},
			},
			{
				Name:  "query",
				Usage: "Run a query on one or multiple projects.",
				Action: func(c *cli.Context) error {

					lang := c.String("lang")
					if lang == "" {
						panic("--lang not set")
					}

					queryFilepath := c.String("query")
					if lang == "" {
						panic("--query not set")
					}

					queryBytes, err := ioutil.ReadFile(queryFilepath)
					if err != nil {
						return err
					}
					queryString := string(queryBytes)

					repoURLsRaw := []string(c.Args())
					hasRepoList := c.IsSet("f")
					if hasRepoList {
						repoListFilepaths := c.StringSlice("f")
						for _, path := range repoListFilepaths {
							err := ReadConfigLinesAsString(path, func(line string) bool {
								repoURLsRaw = append(repoURLsRaw, line)
								return true
							})
							if err != nil {
								return err
							}
						}
					}
					repoURLsRaw = Deduplicate(repoURLsRaw)

					repoURLs := make([]string, 0)
					for _, raw := range repoURLsRaw {
						owner, isWholeUser, err := IsUserOnly(raw)
						if err != nil {
							panic(err)
						}
						if isWholeUser {
							Debugf("Getting list of repos for %s ...", owner)
							repos, err := GithubGetRepoList(owner)
							if err != nil {
								panic(fmt.Errorf("error while getting repo list for user %q: %s", owner, err))
							}
							Debugf("%s has %v repos", owner, len(repos))
							for _, repo := range repos {
								//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
								isFork := repo.GetFork()
								// "Currently we do not support analysis of forks. Consider adding the parent of the fork instead."
								if !isFork {
									repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
								} else {
									Warnf("Skipping fork %s", repo.GetFullName())
								}
							}
						} else {
							parsed, err := ParseGitURL(raw, false)
							if err != nil {
								panic(err)
							}
							repoURLs = append(repoURLs, parsed.URL())
						}
					}

					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}

					alreadyFollowed := func(projectURL string) (*Project, bool) {
						for _, pr := range projects {
							alreadyFollowed := projectURL == pr.ExternalURL.URL
							if alreadyFollowed {
								return pr, true
							}
						}
						return nil, false
					}
					IsProto := func(projectURL string) (*ProtoProject, bool) {
						for _, pr := range protoProjects {
							found := projectURL == pr.CloneURL
							if found {
								return pr, true
							}
						}
						return nil, false
					}
					force := c.Bool("F")
					if force {
						toBeFollowed := make([]string, 0)
						// exclude already-followed projects:
						for _, repoURL := range repoURLs {
							if _, already := alreadyFollowed(repoURL); !already {
								toBeFollowed = append(toBeFollowed, repoURL)
							}
						}
						totalToBeFollowed := len(toBeFollowed)
						Infof("Will follow %v projects...", totalToBeFollowed)
						// follow repos:
						for index, repoURL := range toBeFollowed {
							proto, isProto := IsProto(repoURL)
							if isProto && force {
								protoRebuilder(proto.Key, repoURL)
							} else {
								follower(repoURL, int64(index+1), int64(totalToBeFollowed))
							}
						}
					}

					excluded := c.StringSlice("exclude")

					trimGithubPrefix := func(s string) string {
						return strings.TrimPrefix(s, "https://github.com/")
					}

					projectkeys := make([]string, 0)
					for _, repoURL := range repoURLs {

						_, isProto := IsProto(repoURL)
						if isProto {
							Warnf("%s is proto; skipping", trimGithubPrefix(repoURL))
							continue
						}

						pr, already := alreadyFollowed(repoURL)
						if !already {
							Warnf("%s is not followed; skipping", trimGithubPrefix(repoURL))
						} else {
							isSupportedLanguageForProject := SliceContains(pr.Languages, lang)
							if !isSupportedLanguageForProject {
								Warnf("%s does not have language %s; skipping", trimGithubPrefix(repoURL), lang)
							} else {

								isExcluded := SliceContains(excluded, pr.DisplayName)
								if isExcluded {
									Warnf("%s is excluded; skipping", trimGithubPrefix(repoURL))
								} else {
									projectkeys = append(projectkeys, pr.Key)

								}
							}
						}
					}

					Infof(
						"Sending query %q to be run on %v projects...",
						queryFilepath,
						len(projectkeys),
					)
					queryConfig := &QueryConfig{
						Lang:        lang,
						ProjectKeys: projectkeys,
						QueryString: queryString,
					}
					resp, err := client.Query(queryConfig)
					if err != nil {
						return err
					}

					Infof("See query results at:")

					fmt.Println(resp.GetResultLink())
					return nil
				},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "exclude, e",
						Usage: "Exclude project; example: github/api.",
					},
					&cli.StringFlag{
						Name:  "lang, l",
						Usage: "Language of the query project.",
					},
					&cli.StringFlag{
						Name:  "query, q",
						Usage: "Filepath to .ql query file.",
					},
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
					&cli.BoolFlag{
						Name:  "force, F",
						Usage: "Follow what is not followed.",
					},
				},
			},
		},
	}

	sort.Sort(cli.FlagsByName(app.Flags))
	sort.Sort(cli.CommandsByName(app.Commands))

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func GithubGetRepoList(owner string) ([]*github.Repository, error) {

	owner = strings.TrimSpace(owner)

	// determine whether the owner is a user or an org:
	ownerUser, isUser, err := ghClient.IsOwnerAUser(owner)
	if err != nil {
		return nil, bianconiglio.Contextualize(err,
			"owner", owner,
		)
	}

	var ownerOrg *github.Organization
	var isOrg bool
	if !isUser {
		ownerOrg, isOrg, err = ghClient.IsOwnerAnOrg(owner)
		if err != nil {
			return nil, bianconiglio.Contextualize(err,
				"owner", owner,
			)
		}
		if !isOrg {
			return nil, fmt.Errorf("owner is neither an org nor a common user: %s", owner)
		}
	}

	IsUser := func() bool {
		return ownerUser != nil && ownerUser.GetType() != "Organization"
	}
	_ = IsUser

	IsOrg := func() bool {
		return ownerOrg != nil || (ownerUser != nil && ownerUser.GetType() == "Organization")
	}

	if ownerUser == nil && ownerOrg == nil {
		return nil, bianconiglio.Contextualize(
			errors.New("ownerUser and ownerOrg are both nil"),
			"owner", owner,
		)
	}

	// get list of repos:
	_ = IsOrg()

	repoList := make([]*github.Repository, 0)

	{ // get list of repos:
		if isOrg {
			orgRepos, err := ghClient.ListReposByOrg(owner)
			if err != nil {
				return nil, fmt.Errorf("error while ListReposByOrg: %s", err)
			}
			repoList = append(repoList, orgRepos...)
		} else {
			userRepos, err := ghClient.ListReposByUser(owner)
			if err != nil {
				return nil, fmt.Errorf("error while ListReposByUser: %s", err)
			}
			repoList = append(repoList, userRepos...)
		}
	}

	return repoList, nil
}

func realProjectListToSuggest(projects []*Project) []prompt.Suggest {
	suggests := make([]prompt.Suggest, 0)
	for _, prj := range projects {
		sugg := prompt.Suggest{
			Text:        prj.DisplayName,
			Description: prj.ExternalURL.URL,
		}
		suggests = append(suggests, sugg)
	}

	return suggests
}
func completer(d prompt.Document) []prompt.Suggest {
	s := []prompt.Suggest{
		{Text: "users", Description: "Store the username and age"},
		{Text: "articles", Description: "Store the article text posted by user"},
		{Text: "comments", Description: "Store the text commented to articles"},
	}
	return prompt.FilterContains(s, d.GetWordBeforeCursor(), true)
}

func completerFunc(items []prompt.Suggest) func(prompt.Document) []prompt.Suggest {
	return func(d prompt.Document) []prompt.Suggest {
		return prompt.FilterContains(items, d.GetWordBeforeCursor(), true)
	}
}

func LoadConfigFromFile(filepath string) (*Config, error) {
	jsonFile, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("error while reading config file from %q: %s", filepath, err)
	}

	var conf Config
	err = json.Unmarshal(jsonFile, &conf)
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling config file: %s", err)
	}

	return &conf, nil
}

type Client struct {
	conf *Config
}

func NewClient(conf *Config) (*Client, error) {
	if conf == nil {
		return nil, errors.New("conf is nil")
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}

	cl := &Client{
		conf: conf,
	}
	return cl, nil
}

func (cl *Client) ListFollowedProjects() ([]*Project, []*ProtoProject, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, nil, err
	}

	resp, err := req.Get("https://lgtm.com/internal_api/v0.2/getMyProjects?apiVersion=" + cl.conf.APIVersion)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response ProjectListResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, nil, fmt.Errorf("error while unmarshaling: %s", err)
	}
	projectList := make([]*Project, 0)
	protoProjectList := make([]*ProtoProject, 0)
	for _, envelope := range response.Data {
		prj := envelope.MustGetProject()
		if prj != nil {
			projectList = append(projectList, prj)
		}

		protoPrj := envelope.MustGetProtoProject()
		if protoPrj != nil {
			protoProjectList = append(protoProjectList, protoPrj)
		}
	}

	return projectList, protoProjectList, nil
}

type CommonResponse struct {
	Status string `json:"status"`
}

const STATUS_SUCCESS = "success"

func (cl *Client) UnfollowProject(key string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"project_key": key,
		"apiVersion":  cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/unfollowProject")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}
func (cl *Client) UnfollowProtoProject(key string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"protoproject_key": key,
		"apiVersion":       cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/unfollowProtoproject")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

func (cl *Client) RebuildProtoProject(key string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"config":           "",
		"protoproject_key": key,
		"apiVersion":       cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/rebuildProtoproject")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}
func (cl *Client) FollowProject(u string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"url":        u,
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/followProject")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

func (cl *Client) DeleteProjectSelection(name string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"name":       name,
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/deleteProjectSelection")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

func (cl *Client) CreateProjectSelection(name string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"name":       name,
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/createProjectSelection")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}
func formatStringArray(sl ...string) string {
	marshaled, err := json.Marshal(sl)
	if err != nil {
		panic(err)
	}
	return string(marshaled)
}
func (cl *Client) AddProjectToSelection(selectionID string, projectKeys ...string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"projectSelectionId": selectionID,
		"addedProjects":      formatStringArray(projectKeys...),
		"removedProjects":    "[]",
		"apiVersion":         cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/updateProjectSelection")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}
	if response.Status != STATUS_SUCCESS {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

type SearchSuggestionsResponse struct {
	Status string                  `json:"status"`
	Data   []*SearchSuggestionItem `json:"data"`
}
type SearchSuggestionItem struct {
	Text       string `json:"text"`
	URL        string `json:"url"`
	ProjectKey string `json:"projectKey"`
}

func (cl *Client) GetSearchSuggestions(str string) ([]*SearchSuggestionItem, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getSearchSuggestions?searchSuggestions=%s&apiVersion=%s",
			str,
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response SearchSuggestionsResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	return response.Data, nil
}

type ProjectSelectionListResponse struct {
	Status string                  `json:"status"`
	Data   []*ProjectSelectionBare `json:"data"`
}
type ProjectSelectionBare struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (cl *Client) ListProjectSelections() ([]*ProjectSelectionBare, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}
	req.Data = map[string]string{
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/getUsedProjectSelections")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response ProjectSelectionListResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type ListProjectsInSelectionResponse struct {
	Status string                `json:"status"`
	Data   *ProjectSelectionFull `json:"data"`
}
type Identity struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}
type ProjectSelectionFull struct {
	Identity    Identity `json:"identity"`
	ProjectKeys []string `json:"projectKeys"`
}

func (cl *Client) ListProjectsInSelection(name string) (*ProjectSelectionFull, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getProjectSelectionByName?name=%s&apiVersion=%s",
			name,
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// TODO: catch rate limit error, and wait
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response ListProjectsInSelectionResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type QueryConfig struct {
	Lang        string
	ProjectKeys []string
	QueryString string
}

type QueryResponse struct {
	Status string            `json:"status"`
	Data   QueryResponseData `json:"data"`
}
type Stats struct {
	AllRuns                int `json:"all_runs"`
	Failed                 int `json:"failed"`
	FinishedWithResults    int `json:"finished_with_results"`
	FinishedWithoutResults int `json:"finished_without_results"`
	Incomplete             int `json:"incomplete"`
	PendingSchedulingTasks int `json:"pendingSchedulingTasks"`
}
type QueryResponseData struct {
	Key                  string        `json:"key"`
	QueryText            string        `json:"queryText"`
	ExecutionDate        int64         `json:"executionDate"`
	LanguageKey          string        `json:"languageKey"`
	ProjectKeys          []string      `json:"projectKeys"`
	ProjectSelectionKeys []interface{} `json:"projectSelectionKeys"`
	QueryAllProjects     bool          `json:"queryAllProjects"`
	Stats                Stats         `json:"stats"`
}

//
func (qrd *QueryResponseData) GetResultLink() string {
	return Sf("https://lgtm.com/query/%s/", qrd.Key)
}

func (cl *Client) Query(conf *QueryConfig) (*QueryResponseData, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}
	req.Data = map[string]string{
		"lang":                 conf.Lang,
		"projectKeys":          formatStringArray(conf.ProjectKeys...),
		"projectSelectionKeys": "[]",
		"queryString":          conf.QueryString,
		"queryAllProjects":     "false",
		"guessedLocation":      "",
		"apiVersion":           cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/runQuery")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		//io.Copy(ioutil.Discard, resp.Body)
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response QueryResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return &response.Data, nil
}

var (
	DefaultMaxIdleConnsPerHost = 50
	Timeout                    = 5 * time.Minute
	DefaultKeepAlive           = 180 * time.Second
)

var (
	httpClient = NewHTTP()
)

func NewHTTPTransport() *http.Transport {
	return &http.Transport{
		IdleConnTimeout:     Timeout,
		MaxIdleConnsPerHost: DefaultMaxIdleConnsPerHost,
		Proxy:               http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   Timeout,
			KeepAlive: DefaultKeepAlive,
		}).Dial,
		//TLSClientConfig: &tls.Config{
		//	InsecureSkipVerify: conf.InsecureSkipVerify,
		//},
	}
}

// NewHTTP returns a new Client from the provided config.
// Client is safe for concurrent use by multiple goroutines.
func NewHTTP() *http.Client {
	tr := NewHTTPTransport()

	return &http.Client{
		Timeout:   Timeout,
		Transport: tr,
	}
}

func (cl *Client) newRequest() (*request.Request, error) {
	apiRateLimiter.Take()

	req := request.NewRequest(httpClient)
	req.Headers = map[string]string{
		"authority":        "lgtm.com",
		"accept":           "*/*",
		"lgtm-nonce":       cl.conf.Session.Nonce,
		"dnt":              "1",
		"x-requested-with": "XMLHttpRequest",
		"user-agent":       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/73.0.3683.103 Safari/537.36",
		"sec-fetch-site":   "same-origin",
		"sec-fetch-mode":   "cors",
		"referer":          "https://lgtm.com/dashboard",
		"accept-encoding":  "gzip",
	}

	req.Cookies = map[string]string{
		"lgtm_long_session":  cl.conf.Session.LongSession,
		"lgtm_short_session": cl.conf.Session.ShortSession,
		"_consent_settings":  "accepted",
	}

	return req, nil
}

type LGTMSession struct {
	Nonce        string `json:"nonce"`
	ShortSession string `json:"short_session"`
	LongSession  string `json:"long_session"`
}

// Validate validates
func (sess *LGTMSession) Validate() error {
	if sess.Nonce == "" {
		return errors.New("sess.Nonce is not set")
	}
	if sess.ShortSession == "" {
		return errors.New("sess.ShortSession is not set")
	}
	if sess.LongSession == "" {
		return errors.New("sess.LongSession is not set")
	}
	return nil
}

type Config struct {
	APIVersion string        `json:"api_version"`
	Session    *LGTMSession  `json:"session,omitempty"`
	GitHub     *GithubConfig `json:"github,omitempty"`
}

type GithubConfig struct {
	Token string `json:"token"`
}

// Validate validates
func (conf *Config) Validate() error {
	if conf.APIVersion == "" {
		return errors.New("conf.APIVersion not provided")
	}

	if conf.Session == nil {
		return errors.New("conf.Session is nil")
	}

	return conf.Session.Validate()
}

type Envelope struct {
	RawRealProject     interface{} `json:"realProject"`
	RawProtoProject    interface{} `json:"protoproject"`
	parsedproject      *Project
	parsedProtoProject *ProtoProject
}

//
func (env *Envelope) MustGetProject() *Project {
	if env.parsedproject != nil {
		return env.parsedproject
	}
	if env.RawRealProject == nil {
		return nil
	}

	var slice []interface{}
	err := TranscodeJSON(env.RawRealProject, &slice)
	if err != nil {
		panic(err)
	}
	firstObjectInterface := slice[0]

	var parsedproject Project
	err = TranscodeJSON(firstObjectInterface, &parsedproject)
	if err != nil {
		panic(err)
	}
	env.parsedproject = &parsedproject
	return env.parsedproject
}

func (env *Envelope) MustGetProtoProject() *ProtoProject {
	if env.parsedProtoProject != nil {
		return env.parsedProtoProject
	}
	if env.RawProtoProject == nil {
		return nil
	}

	var proto ProtoProject
	err := TranscodeJSON(env.RawProtoProject, &proto)
	if err != nil {
		panic(err)
	}
	env.parsedProtoProject = &proto

	return env.parsedProtoProject
}

type ProtoProject struct {
	Key              string `json:"key"`
	DisplayName      string `json:"displayName"`
	State            string `json:"state"`
	BuildAttemptKey  string `json:"buildAttemptKey"`
	NextBuildStarted bool   `json:"nextBuildStarted"`
	CloneURL         string `json:"cloneUrl"`
}

type Project struct {
	Key                string               `json:"key"`
	Languages          []string             `json:"languages"`
	TotalLanguageChurn []TotalLanguageChurn `json:"totalLanguageChurn"`
	RepoProvider       string               `json:"repoProvider"`
	DisplayName        string               `json:"displayName"`
	Slug               string               `json:"slug"`
	ExternalURL        ExternalURL          `json:"externalURL"`
	AdminURL           string               `json:"adminURL"`
	Modes              Modes                `json:"modes"`
}
type TotalLanguageChurn struct {
	Lang  string `json:"lang"`
	Churn int    `json:"churn"`
}
type ExternalURL struct {
	URL   string `json:"url"`
	Name  string `json:"name"`
	Theme string `json:"theme"`
}
type Modes map[string]string

type ProjectListResponse struct {
	Status string      `json:"status"`
	Data   []*Envelope `json:"data"`
}

// Info should be used to describe the example commands that are about to run.
func Info(format string, args ...interface{}) {
	fmt.Printf("\x1b[34;1m%s\x1b[0m\n", fmt.Sprintf(format, args...))
}

// Warning should be used to display a warning
func Warning(format string, args ...interface{}) {
	fmt.Printf("\x1b[36;1m%s\x1b[0m\n", fmt.Sprintf(format, args...))
}

func HasPrefix(s string, prefix string) bool {
	return strings.HasPrefix(s, prefix)
}
func IsEmptyHostError(err error) bool {
	if e, ok := err.(*url.Error); ok && e.Err.Error() == "empty host" {
		return true
	}
	return false
}

// TrimSlashes trims initial and final slashes.
func TrimSlashes(s string) string {
	return strings.Trim(s, "/")
}

// IsUserOnly returns a bool telling whether only the user is specified (i.e. whole account, without a particular repo name).
func IsUserOnly(rawURL string) (string, bool, error) {
	grl, err := ParseGitURL(rawURL, false)
	if err != nil {
		return "", false, err
	}

	isWholeUser := grl.Repo == ""
	if isWholeUser {
		return grl.User, isWholeUser, nil
	}
	return "", false, nil
}

type GitURL struct {
	Scheme   string
	Hostname string
	Port     string

	User string
	Repo string
}

func (grl *GitURL) URL() string {
	if grl.Repo != "" {
		return grl.Scheme + "://" + grl.Hostname + ":" + grl.Port + "/" + grl.User + "/" + grl.Repo
	}
	return grl.Scheme + "://" + grl.Hostname + ":" + grl.Port + "/" + grl.User
}

// ParseGitURL verifies and splits a URL into the git repo info (hostname, userr account name, repo name)
func ParseGitURL(rawURL string, mustHaveRepoName bool) (*GitURL, error) {
	//rawURL = TrimSlashes(rawURL)
	rawURL = strings.TrimSuffix(rawURL, ".git")
	{
		if CountSlashes(rawURL) == 1 || CountSlashes(rawURL) == 0 {
			rawURL = TrimSlashes(defaultHost) + "/" + TrimSlashes(rawURL)
		}
	}
	parsedURL, err := urlx.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	final := &GitURL{}

	final.Scheme = parsedURL.Scheme
	final.Hostname = SanitizeFileNamePart(parsedURL.Hostname())
	final.Port = parsedURL.Port()

	path := TrimSlashes(parsedURL.Path)

	slashCount := strings.Count(path, "/")

	if !mustHaveRepoName {
		if slashCount > 1 {
			return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
		}

		if slashCount > 0 {
			slice := strings.Split(path, "/")
			if len(slice) < 1 {
				return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
			}
			final.User = SanitizeFileNamePart(strings.TrimSpace(slice[0]))
			if len(slice) > 1 {
				final.Repo = SanitizeFileNamePart(strings.TrimSpace(slice[1]))
			}
		}

		if slashCount == 0 {
			final.User = SanitizeFileNamePart(path)
		}

	} else {
		if slashCount != 1 {
			return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
		}

		slice := strings.Split(path, "/")
		if len(slice) != 2 {
			return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
		}
		final.User = SanitizeFileNamePart(strings.TrimSpace(slice[0]))
		final.Repo = SanitizeFileNamePart(strings.TrimSpace(slice[1]))
	}

	if len(final.User) == 0 {
		return nil, errors.New("user not specified")
	}
	if len(final.Repo) == 0 && mustHaveRepoName {
		return nil, errors.New("repo not specified")
	}

	return final, nil
}
func CountSlashes(s string) int {
	return strings.Count(s, "/")
}
