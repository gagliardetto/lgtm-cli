package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gagliardetto/request"
	. "github.com/gagliardetto/utilz"
)

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
		return nil, nil, formatNotOKStatusCodeError(resp)
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

const STATUS_SUCCESS_STRING = "success"

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
		return formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
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
		return formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

type FollowProjectResponse struct {
	Status string    `json:"status"`
	Data   *Envelope `json:"data"`
}

func (cl *Client) FollowProject(u string) (*Envelope, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}
	req.Data = map[string]string{
		"url":        u,
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/followProject")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response FollowProjectResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
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
		return formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
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
		return formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}
func formatStringArray(sl ...string) string {
	if len(sl) == 0 {
		return "[]"
	}
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
		return formatNotOKStatusCodeError(resp)
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
	if response.Status != STATUS_SUCCESS_STRING {
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
		return nil, formatNotOKStatusCodeError(resp)
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
		return nil, formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
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
		return nil, formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type QueryConfig struct {
	Lang                 string
	ProjectKeys          []string
	ProjectSelectionKeys []string
	QueryString          string
}

type QueryResponse struct {
	Status string            `json:"status"`
	Data   QueryResponseData `json:"data"`
}
type QueryResponseStats struct {
	AllRuns                int `json:"all_runs"`
	Failed                 int `json:"failed"`
	FinishedWithResults    int `json:"finished_with_results"`
	FinishedWithoutResults int `json:"finished_without_results"`
	Incomplete             int `json:"incomplete"`
	PendingSchedulingTasks int `json:"pendingSchedulingTasks"`
}
type QueryResponseData struct {
	Key                  string             `json:"key"`
	QueryText            string             `json:"queryText"`
	ExecutionDate        int64              `json:"executionDate"`
	LanguageKey          string             `json:"languageKey"`
	ProjectKeys          []string           `json:"projectKeys"`
	ProjectSelectionKeys []string           `json:"projectSelectionKeys"`
	QueryAllProjects     bool               `json:"queryAllProjects"`
	Stats                QueryResponseStats `json:"stats"`
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
		"projectSelectionKeys": formatStringArray(conf.ProjectSelectionKeys...),
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
		return nil, formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return &response.Data, nil
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

// IsKnown returns whether the projects was already known to lgtm.com
func (env *Envelope) IsKnown() bool {
	isFirstBuild := env.MustGetProject() == nil && env.MustGetProtoProject() != nil
	return !isFirstBuild
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

func (pr *Project) SupportsLanguage(lang string) bool {
	return SliceContains(pr.Languages, lang)
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
		return formatNotOKStatusCodeError(resp)
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

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

const (
	LangGo         = "go"
	LangCPP        = "cpp"
	LangCSharp     = "csharp"
	LangJava       = "java"
	LangJavaScript = "javascript"
	LangPython     = "python"
)

// NewBuildAttempt allows to attempt a build for a language NOT previously built.
func (cl *Client) NewBuildAttempt(projectKey string, lang string) error {
	req, err := cl.newRequest()
	if err != nil {
		return err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/newBuildAttempt?projectKey=%s&language=%s&apiVersion=%s",
			projectKey,
			lang,
			cl.conf.APIVersion,
		))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return formatNotOKStatusCodeError(resp)
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
	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}
	return nil
}

// RequestTestBuild triggers re-build for the specified language(s).
func (cl *Client) RequestTestBuild(urlIdentifier string, langs ...string) error {
	req, err := cl.newRequest()
	if err != nil {
		return err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/"+
				"urlIdentifier=%s&languages=%s&config=&apiVersion=%s",
			urlIdentifier,
			url.QueryEscape(formatStringArray(langs...)),
			cl.conf.APIVersion,
		))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return formatNotOKStatusCodeError(resp)
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
	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}
	return nil
}

type GetProjectLatestStateStatsResponse struct {
	Status string                `json:"status"`
	Data   *LatestStateStatsData `json:"data"`
}
type Rating struct {
	Score    float64 `json:"score"`
	Grade    string  `json:"grade"`
	RawGrade float64 `json:"rawGrade"`
}
type RevisionName struct {
	Value       string `json:"value"`
	PrettyValue string `json:"prettyValue"`
}
type SecurityAwareness struct {
	Score             float64 `json:"score"`
	NumSecurityAlerts int     `json:"numSecurityAlerts"`
	Grade             string  `json:"grade"`
	Percentile        float64 `json:"percentile"`
}
type LanguageStates struct {
	Lang              string            `json:"lang"`
	SnapshotDate      int64             `json:"snapshotDate"`
	TotalAlerts       int               `json:"totalAlerts"`
	TotalLines        int               `json:"totalLines"`
	Rating            Rating            `json:"rating,omitempty"`
	RevisionName      RevisionName      `json:"revisionName"`
	SecurityAwareness SecurityAwareness `json:"securityAwareness,omitempty"`
}
type LatestStateStatsData struct {
	NumContributors int              `json:"numContributors"`
	LanguageStates  []LanguageStates `json:"languageStates"`
}

func (cl *Client) GetProjectLatestStateStats(projectKey string) (*LatestStateStatsData, error) {
	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getProjectLatestStateStats?key=%s&apiVersion=%s",
			projectKey,
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response GetProjectLatestStateStatsResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type GetProjectsByKeyResponse struct {
	Status string                        `json:"status"`
	Data   *GetProjectsByKeyResponseData `json:"data"`
}

type GetProjectsByKeyResponseData struct {
	FullProjects map[string]*Project    `json:"fullProjects"`
	AnonProjects map[string]interface{} `json:"anonProjects"`
}

func (data *GetProjectsByKeyResponseData) GetProject(key string) *Project {
	val, ok := data.FullProjects[key]
	if ok {
		return val
	}
	return nil
}

func (cl *Client) GetProjectsByKey(keys ...string) (*GetProjectsByKeyResponseData, error) {
	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getProjectsByKey?keys=%s&apiVersion=%s",
			formatStringArray(keys...),
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response GetProjectsByKeyResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}
func isAlreadyFollowedProject(projects []*Project, projectURL string) (*Project, bool) {
	for _, pr := range projects {
		alreadyFollowed := ToLower(projectURL) == ToLower(pr.ExternalURL.URL)
		if alreadyFollowed {
			return pr, true
		}
	}
	return nil, false
}

func isAlreadyFollowedProto(protoProjects []*ProtoProject, projectURL string) (*ProtoProject, bool) {
	withDotGitSuffix := ""
	if !strings.HasSuffix(projectURL, ".git") {
		withDotGitSuffix = projectURL + ".git"
	} else {
		withDotGitSuffix = projectURL
	}
	for _, pr := range protoProjects {
		alreadyFollowed := (ToLower(projectURL) == ToLower(pr.CloneURL)) || (ToLower(withDotGitSuffix) == ToLower(pr.CloneURL))
		if alreadyFollowed {
			return pr, true
		}
	}
	return nil, false
}

type OrderBy string

const (
	OrderByNumAlerts    OrderBy = "num_alerts"
	OrderByRunTime      OrderBy = "run_time"
	OrderByProjectSize  OrderBy = "loc"
	OrderByAlertDensity OrderBy = "alert_density"
)

func (cl *Client) GetQueryResults(queryID string, orderBy OrderBy, startCursor string) (*GetQueryResultsResponseData, error) {
	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	base := "https://lgtm.com/internal_api/v0.2/getQueryResults"
	vals := url.Values{}
	{
		vals.Set("queryId", queryID)
		vals.Set("limit", "10")
		vals.Set("orderBy", string(orderBy))
		if startCursor != "" {
			vals.Set("startCursor", "")
		}
		vals.Set("apiVersion", cl.conf.APIVersion)
	}

	resp, err := req.Get(base + "?" + vals.Encode())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response GetQueryResultsResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type GetQueryResultsResponse struct {
	Status string                       `json:"status"`
	Data   *GetQueryResultsResponseData `json:"data"`
}
type SrcVersion struct {
	Value       string `json:"value"`
	PrettyValue string `json:"prettyValue"`
}
type GetQueryResultsResponseStats struct {
	QueryRunKey          string   `json:"queryRunKey"`
	NumResults           int      `json:"numResults"`
	NumExcludedResults   int      `json:"numExcludedResults"`
	ResultsWereTruncated bool     `json:"resultsWereTruncated"`
	Columns              []string `json:"columns"`
	IsInAlertFormat      bool     `json:"isInAlertFormat"`
	HasAlertResults      bool     `json:"hasAlertResults"`
	NumAlerts            int      `json:"numAlerts"`
}
type GetQueryResultsResponseItem struct {
	Key         string                        `json:"key"`
	ProjectKey  string                        `json:"projectKey"`
	Lang        string                        `json:"lang"`
	SnapshotKey string                        `json:"snapshotKey"`
	SrcVersion  *SrcVersion                   `json:"srcVersion"`
	Done        bool                          `json:"done"`
	Stats       *GetQueryResultsResponseStats `json:"stats,omitempty"`
	Error       string                        `json:"error,omitempty"`
}
type GetQueryResultsResponseData struct {
	Cursor string                         `json:"cursor"`
	Items  []*GetQueryResultsResponseItem `json:"items"`
}
