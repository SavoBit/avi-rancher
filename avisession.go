package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	// "net/http/httputil"
	"reflect"
)

type aviResult struct {
	// Code should match the HTTP status code.
	Code int `json:"code"`

	// Message should contain a short description of the result of the requested
	// operation.
	Message *string `json:"message"`
}

// AviError represents an error resulting from a request to the Avi Controller
type AviError struct {
	// aviresult holds the standard header (code and message) that is included in
	// responses from Avi.
	aviResult

	// verb is the HTTP verb (GET, POST, PUT, PATCH, or DELETE) that was
	// used in the request that resulted in the error.
	verb string

	// url is the URL that was used in the request that resulted in the error.
	url string

	// httpStatusCode is the HTTP response status code (e.g., 200, 404, etc.).
	httpStatusCode int

	// err contains a descriptive error object for error cases other than HTTP
	// errors (i.e., non-2xx responses), such as socket errors or malformed JSON.
	err error
}

// Error implements the error interface.
func (err AviError) Error() string {
	var msg string

	if err.err != nil {
		msg = fmt.Sprintf("error: %v", err.err)
	} else if err.Message != nil {
		msg = fmt.Sprintf("HTTP code: %d; error from Avi: %s",
			err.httpStatusCode, *err.Message)
	} else {
		msg = fmt.Sprintf("HTTP code: %d.", err.httpStatusCode)
	}

	return fmt.Sprintf("Encountered an error on %s request to URL %s: %s",
		err.verb, err.url, msg)
}

type AviSession struct {
	// host specifies the hostname or IP address of the Avi Controller
	host string

	// username specifies the username with which we should authenticate with the
	// Avi Controller.
	username string

	// password specifies the password with which we should authenticate with the
	// Avi Controller.
	password string

	// insecure specifies whether we should perform strict certificate validation
	// for connections to the Avi Controller.
	insecure bool

	// optional tenant string to use for API request
	Tenant string

	// internal: session id for this session
	sessionid string

	// internal: csrf_token for this session
	csrf_token string

	// internal: referer field string to use in requests
	prefix string

	// internal: Controller version
	cont_version string
}

func NewAviSession(host string, username string, password string, insecure bool) *AviSession {
	avisess := &AviSession{
		host:     host,
		username: username,
		password: password,
		insecure: insecure,
	}
	avisess.sessionid = ""
	avisess.csrf_token = ""
	avisess.prefix = "https://" + avisess.host + "/"
	avisess.Tenant = ""
	avisess.cont_version = ""
	return avisess
}

func (avisession *AviSession) InitiateSession() error {
	log.Info("Initiating session %s, %s, %s", avisession.prefix, avisession.username, avisession.insecure)
	if avisession.insecure == true {
		log.Warn("Strict certificate verification is *DISABLED*")
	}

	// initiate http session here
	// first set the csrf token
	res, _ := avisession.Get("")

	// now login to get session_id
	cred := make(map[string]string)
	cred["username"] = avisession.username
	cred["password"] = avisession.password
	res, rerror := avisession.Post("login", cred)
	if rerror != nil {
		log.Warn("Unable to initiate HTTP(S) session with Avi: ", rerror)
		return rerror
	}
	// now session id is set too

	version := res.(map[string]interface{})["version"]
	avisession.cont_version = (version.(map[string]interface{})["Version"]).(string)
	log.Info("response: ", res)
	if res != nil && reflect.TypeOf(res).Kind() != reflect.String {
		log.Info("results: ", res.(interface{}), " error: ", rerror)
	}

	return nil
}

//
// Helper routines for REST calls.
//

// rest_request makes a REST request to the Avi Controller's REST API.
// Returns a byte[] if successful
func (avi *AviSession) rest_request(verb string, uri string, payload interface{}) ([]byte, error) {
	var result []byte
	url := avi.prefix + uri

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: avi.insecure},
	}

	errorResult := AviError{verb: verb, url: url}

	var payloadIO io.Reader
	if payload != nil {
		jsonStr, err := json.Marshal(payload)
		if err != nil {
			return result, AviError{verb: verb, url: url, err: err}
		}
		payloadIO = bytes.NewBuffer(jsonStr)
	}

	req, err := http.NewRequest(verb, url, payloadIO)
	if err != nil {
		errorResult.err = fmt.Errorf("http.NewRequest failed: %v", err)
		return result, errorResult
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if avi.csrf_token != "" {
		req.Header["X-CSRFToken"] = []string{avi.csrf_token}
		req.AddCookie(&http.Cookie{Name: "csrftoken", Value: avi.csrf_token})
	}
	if avi.prefix != "" {
		req.Header.Set("Referer", avi.prefix)
	}
	if avi.Tenant != "" {
		req.Header.Set("X-Avi-Tenant", avi.Tenant)
	}
	if avi.sessionid != "" {
		req.AddCookie(&http.Cookie{Name: "sessionid", Value: avi.sessionid})
	}

	req.Header.Set("X-AVI-VERSION", avi.cont_version)
	// dump, err := httputil.DumpRequestOut(req, true)
	// debug(dump, err)
	client := &http.Client{Transport: tr}

	resp, err := client.Do(req)
	if err != nil {
		errorResult.err = fmt.Errorf("client.Do failed: %v", err)
		return result, errorResult
	}

	defer resp.Body.Close()

	errorResult.httpStatusCode = resp.StatusCode

	// collect cookies from the resp
	for _, cookie := range resp.Cookies() {
		log.Info("cookie: ", cookie)
		if cookie.Name == "csrftoken" {
			avi.csrf_token = cookie.Value
			log.Info("Set the csrf token to ", avi.csrf_token)
		}
		if cookie.Name == "sessionid" {
			avi.sessionid = cookie.Value
		}
	}

	if resp.StatusCode == 419 {
		// session got reset; try again
		return avi.rest_request(verb, uri, payload)
	}

	if resp.StatusCode == 401 && len(avi.sessionid) != 0 && uri != "login" {
		// session expired; initiate session and then retry the request
		avi.InitiateSession()
		return avi.rest_request(verb, uri, payload)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Info("Error: ", resp)
		bres, berr := ioutil.ReadAll(resp.Body)
		if berr == nil {
			mres, _ := ConvertAviResponseToMapInterface(bres)
			log.Info("Error resp: ", mres)
		}
		return result, errorResult
	}

	if resp.StatusCode == 204 {
		// no content in the response
		return result, nil
	}

	result, err = ioutil.ReadAll(resp.Body)
	return result, err
}

func ConvertAviResponseToMapInterface(resbytes []byte) (interface{}, error) {
	var result interface{}
	err := json.Unmarshal(resbytes, &result)
	return result, err
}

type AviCollectionResult struct {
	Count   int
	Results []json.RawMessage
}

func ConvertBytesToSpecificInterface(resbytes []byte, result interface{}) error {
	err := json.Unmarshal(resbytes, result)
	return err
}

func debug(data []byte, err error) {
	if err == nil {
		fmt.Printf("%s\n\n", data)
	} else {
		log.Fatalf("%s\n\n", err)
	}
}

func (avi *AviSession) rest_request_interface_response(verb string, url string,
	payload interface{}) (interface{}, error) {
	res, rerror := avi.rest_request(verb, url, payload)
	if rerror != nil || res == nil {
		return res, rerror
	}
	return ConvertAviResponseToMapInterface(res)
}

// get issues a GET request against the avi REST API.
func (avi *AviSession) Get(uri string) (interface{}, error) {
	return avi.rest_request_interface_response("GET", uri, nil)
}

// post issues a POST request against the avi REST API.
func (avi *AviSession) Post(uri string, payload interface{}) (interface{}, error) {
	return avi.rest_request_interface_response("POST", uri, payload)
}

// put issues a PUT request against the avi REST API.
func (avi *AviSession) Put(uri string, payload interface{}) (interface{}, error) {
	return avi.rest_request_interface_response("PUT", uri, payload)
}

// delete issues a DELETE request against the avi REST API.
func (avi *AviSession) Delete(uri string) (interface{}, error) {
	return avi.rest_request_interface_response("DELETE", uri, nil)
}

// delete issues a DELETE request against the avi REST API.
func (avi *AviSession) Del(uri string, payload interface{}) (interface{}, error) {
	return avi.rest_request_interface_response("DELETE", uri, payload)
}

// get issues a GET request against the avi REST API.
func (avi *AviSession) GetCollection(uri string) (AviCollectionResult, error) {
	var result AviCollectionResult
	res, rerror := avi.rest_request("GET", uri, nil)
	if rerror != nil || res == nil {
		return result, rerror
	}
	err := json.Unmarshal(res, &result)
	return result, err
}

func (avi *AviSession) PostRaw(uri string, payload interface{}) ([]byte, error) {
	return avi.rest_request("POST", uri, payload)
}

func (avi *AviSession) GetResourceByName(resource, objname string) (map[string]interface{}, error) {
	resp := make(map[string]interface{})
	res, err := avi.GetCollection("/api/" + resource + "?name=" + objname)
	if err != nil {
		log.Infof("Avi object exists check (res: %s, name: %s) failed: %v", resource, objname, res)
		return resp, err
	}

	if res.Count == 0 {
		return resp, fmt.Errorf("Resource name %s of type %s does not exist on the Avi Controller",
			objname, resource)
	}
	nres, err := ConvertAviResponseToMapInterface(res.Results[0])
	if err != nil {
		log.Infof("Resource unmarshal failed: %v", string(res.Results[0]))
		return resp, err
	}
	return nres.(map[string]interface{}), nil
}

func (avi *AviSession) GetCloudRef(cloudName string) (string, error) {
	cloud, err := avi.GetResourceByName("cloud", cloudName)
	if err != nil {
		return "", err
	}

	return cloud["url"].(string), nil
}

func (avi *AviSession) GetApplicationProfileRef(app string) (string, error) {
	appprofile, err := avi.GetResourceByName("applicationprofile", app)
	if err != nil {
		return "", err
	}

	return appprofile["url"].(string), nil
}

func (avi *AviSession) GetTenantRef(ten string) (string, error) {
	tenant, err := avi.GetResourceByName("tenant", ten)
	if err != nil {
		return "", err
	}

	return tenant["url"].(string), nil
}

func (avi *AviSession) GetNetworkProfileRef(net string) (string, error) {
	netprofile, err := avi.GetResourceByName("networkprofile", net)
	if err != nil {
		return "", err
	}

	return netprofile["url"].(string), nil
}

func (avi *AviSession) GetHealthMonitorRef(mon string) (string, error) {
	hm, err := avi.GetResourceByName("healthmonitor", mon)
	if err != nil {
		return "", err
	}

	return hm["url"].(string), nil
}

func (avi *AviSession) GetSSLref(ssl string) (string, error) {
	ssl_cert, err := avi.GetResourceByName("sslkeyandcertificate", ssl)
	if err != nil {
		return "", err
	}

	return ssl_cert["url"].(string), nil
}
