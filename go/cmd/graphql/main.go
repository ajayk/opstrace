// Copyright 2021 Opstrace, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	"github.com/opstrace/opstrace/go/pkg/graphql"
)

var (
	credentialClient graphql.CredentialAccess
	exporterClient   graphql.ExporterAccess
)

func main() {
	var loglevel string
	flag.StringVar(&loglevel, "loglevel", "info", "error|info|debug")
	var listenAddress string
	flag.StringVar(&listenAddress, "listen", "", "")

	flag.Parse()

	level, lerr := log.ParseLevel(loglevel)
	if lerr != nil {
		log.Fatalf("bad --loglevel: %s", lerr)
	}
	log.SetLevel(level)

	if listenAddress == "" {
		log.Fatalf("missing required --listen")
	}
	log.Infof("listen address: %s", listenAddress)

	graphqlEndpoint := os.Getenv("GRAPHQL_ENDPOINT")
	if graphqlEndpoint == "" {
		// Try default (dev environment)
		graphqlEndpoint = "http://localhost:8080/v1/graphql"
		log.Warnf("missing GRAPHQL_ENDPOINT, trying %s", graphqlEndpoint)
	}
	graphqlEndpointURL, uerr := url.Parse(graphqlEndpoint)
	if uerr != nil {
		log.Fatalf("bad GRAPHQL_ENDPOINT: %s", uerr)
	}
	log.Infof("graphql URL: %v", graphqlEndpointURL)

	graphqlSecret := os.Getenv("HASURA_GRAPHQL_ADMIN_SECRET")
	if graphqlSecret == "" {
		log.Fatalf("missing required HASURA_GRAPHQL_ADMIN_SECRET")
	}

	credentialClient = graphql.NewCredentialAccess(graphqlEndpointURL, graphqlSecret)
	exporterClient = graphql.NewExporterAccess(graphqlEndpointURL, graphqlSecret)

	router := mux.NewRouter()
	router.Handle("/metrics", promhttp.Handler())

	// Specify exact paths, but manually allow with and without a trailing '/'

	credentials := router.PathPrefix("/api/v1/credentials").Subrouter()
	setupAPI(credentials, listCredentials, writeCredentials, getCredential, deleteCredential)

	exporters := router.PathPrefix("/api/v1/exporters").Subrouter()
	setupAPI(exporters, listExporters, writeExporters, getExporter, deleteExporter)

	log.Fatalf("terminated: %v", http.ListenAndServe(listenAddress, router))
}

// setupAPI configures GET/POST/DELETE endpoints for the provided handler callbacks.
// The paths are configured to be exact, with optional trailing slashes.
func setupAPI(
	router *mux.Router,
	listFunc func(http.ResponseWriter, *http.Request),
	writeFunc func(http.ResponseWriter, *http.Request),
	getFunc func(http.ResponseWriter, *http.Request),
	deleteFunc func(http.ResponseWriter, *http.Request),
) {
	router.HandleFunc("", listFunc).Methods("GET")
	router.HandleFunc("/", listFunc).Methods("GET")
	router.HandleFunc("", writeFunc).Methods("POST")
	router.HandleFunc("/", writeFunc).Methods("POST")
	router.HandleFunc("/{name}", getFunc).Methods("GET")
	router.HandleFunc("/{name}/", getFunc).Methods("GET")
	router.HandleFunc("/{name}", deleteFunc).Methods("DELETE")
	router.HandleFunc("/{name}/", deleteFunc).Methods("DELETE")
}

// Information about a credential. Custom type which omits the tenant field.
// This also given some extra protection that the value isn't disclosed,
// even if it was mistakenly added to the underlying graphql interface.
type CredentialInfo struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type,omitempty"`
	CreatedAt string `yaml:"created_at,omitempty"`
	UpdatedAt string `yaml:"updated_at,omitempty"`
}

func listCredentials(w http.ResponseWriter, r *http.Request) {
	tenant, err := getTenant(r)
	if err != nil {
		log.Warn("List credentials: Invalid tenant")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp, err := credentialClient.List(tenant)
	if err != nil {
		log.Warnf("Listing credentials for tenant %s failed: %s", tenant, err)
		http.Error(w, fmt.Sprintf(
			"Listing credentials for tenant %s failed: %s", tenant, err,
		), http.StatusInternalServerError)
		return
	}

	log.Debugf("Listing %d credentials for tenant %s", len(resp.Credential), tenant)

	encoder := yaml.NewEncoder(w)
	for _, credential := range resp.Credential {
		encoder.Encode(CredentialInfo{
			Name:      credential.Name,
			Type:      credential.Type,
			CreatedAt: credential.CreatedAt,
			UpdatedAt: credential.UpdatedAt,
		})
	}
}

// Full credential entry (with secret value) received from a POST request.
type Credential struct {
	Name  string      `yaml:"name"`
	Type  string      `yaml:"type"`
	Value interface{} `yaml:"value"` // nested yaml, or payload string, depending on type
}

func writeCredentials(w http.ResponseWriter, r *http.Request) {
	tenant, err := getTenant(r)
	if err != nil {
		log.Warn("Write credentials: Invalid tenant")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	decoder := yaml.NewDecoder(r.Body)
	// Return error for unrecognized or duplicate fields in the input
	decoder.SetStrict(true)

	// Collect list of existing names so that we can decide between insert vs update
	existingTypes := make(map[string]string)
	resp, err := credentialClient.List(tenant)
	if err != nil {
		log.Warnf("Listing credentials for tenant %s failed: %s", tenant, err)
		http.Error(w, fmt.Sprintf(
			"Listing credentials for tenant %s failed: %s", tenant, err,
		), http.StatusInternalServerError)
		return
	}
	for _, credential := range resp.Credential {
		existingTypes[credential.Name] = credential.Type
	}

	now := nowTimestamp()

	var inserts []graphql.CredentialInsertInput
	var updates []graphql.UpdateCredentialVariables
	for {
		var yamlCredential Credential
		err := decoder.Decode(&yamlCredential)
		if err != nil {
			if err != io.EOF {
				log.Debugf("Decoding credential input at index=%d failed: %s", len(inserts)+len(updates), err)
				http.Error(w, fmt.Sprintf(
					"Decoding credential input at index=%d failed: %s", len(inserts)+len(updates), err,
				), http.StatusBadRequest)
				return
			}
			break
		}
		name := graphql.String(yamlCredential.Name)
		credType := graphql.String(yamlCredential.Type)
		value, err := validateCredValue(yamlCredential.Name, yamlCredential.Type, yamlCredential.Value)
		if err != nil {
			log.Debugf("Invalid credential value format: %s", err)
			http.Error(w, fmt.Sprintf("Credential format validation failed: %s", err), http.StatusBadRequest)
			return
		}
		if existingType, ok := existingTypes[yamlCredential.Name]; ok {
			// Explicitly check and complain if the user tries to change the credential type
			if yamlCredential.Type != "" && existingType != yamlCredential.Type {
				log.Debugf("Invalid credential '%s' type change", yamlCredential.Name)
				http.Error(w, fmt.Sprintf(
					"Credential '%s' type cannot be updated (current=%s, updated=%s)",
					yamlCredential.Name, existingType, yamlCredential.Type,
				), http.StatusBadRequest)
				return
			}
			// TODO check for no-op updates and skip them (and avoid unnecessary changes to UpdatedAt)
			updates = append(updates, graphql.UpdateCredentialVariables{
				Name:      name,
				Value:     *value,
				UpdatedAt: now,
			})
		} else {
			inserts = append(inserts, graphql.CredentialInsertInput{
				Name:      &name,
				Type:      &credType,
				Value:     value,
				CreatedAt: &now,
				UpdatedAt: &now,
			})
		}
	}

	if len(inserts)+len(updates) == 0 {
		log.Debugf("Writing credentials: No data provided")
		http.Error(w, "Missing credential YAML data in request body", http.StatusBadRequest)
		return
	}

	log.Debugf("Writing credentials: %d insert, %d update", len(inserts), len(updates))

	if len(inserts) != 0 {
		err := credentialClient.Insert(tenant, inserts)
		if err != nil {
			log.Warnf("Insert: %d credentials failed: %s", len(inserts), err)
			http.Error(w, fmt.Sprintf("Creating %d credentials failed: %s", len(inserts), err), http.StatusInternalServerError)
			return
		}
	}
	if len(updates) != 0 {
		for _, update := range updates {
			err := credentialClient.Update(tenant, update)
			if err != nil {
				log.Warnf("Update: Credential %s failed: %s", update.Name, err)
				http.Error(w, fmt.Sprintf("Updating credential %s failed: %s", update.Name, err), http.StatusInternalServerError)
				return
			}
		}
	}
}

type AWSCredentialValue struct {
	AwsAccessKeyID     string `json:"AWS_ACCESS_KEY_ID"`
	AwsSecretAccessKey string `json:"AWS_SECRET_ACCESS_KEY"`
}

func validateCredValue(credName string, credType string, credValue interface{}) (*graphql.Json, error) {
	switch credType {
	case "aws-key":
		// Expect regular YAML fields (not as a nested string)
		errfmt := "expected %s credential '%s' value to contain YAML string fields: " +
			"AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY (%s)"
		switch v := credValue.(type) {
		case map[interface{}]interface{}:
			if len(v) != 2 {
				return nil, fmt.Errorf(errfmt, credType, credName, "wrong size")
			}
			key, keyok := v["AWS_ACCESS_KEY_ID"]
			val, valok := v["AWS_SECRET_ACCESS_KEY"]
			if len(v) != 2 || !keyok || !valok {
				return nil, fmt.Errorf(errfmt, credType, credName, "missing fields")
			}
			keystr, keyok := key.(string)
			valstr, valok := val.(string)
			if !keyok || !valok {
				return nil, fmt.Errorf(errfmt, credType, credName, "non-string fields")
			}
			json, err := json.Marshal(AWSCredentialValue{
				AwsAccessKeyID:     keystr,
				AwsSecretAccessKey: valstr,
			})
			if err != nil {
				return nil, fmt.Errorf(errfmt, credType, credName, "failed to reserialize as JSON")
			}
			gjson := graphql.Json(json)
			return &gjson, nil
		default:
			return nil, fmt.Errorf(errfmt, credType, credName, "expected a map")
		}
	case "gcp-service-account":
		// Expect string containing a valid JSON payload
		switch v := credValue.(type) {
		case string:
			if !json.Valid([]byte(v)) {
				return nil, fmt.Errorf("%s credential '%s' value is not a valid JSON string", credType, credName)
			}
			gjson := graphql.Json(v)
			return &gjson, nil
		default:
			return nil, fmt.Errorf("expected %s credential '%s' value to be a JSON string", credType, credName)
		}
	default:
		return nil, fmt.Errorf("unsupported credential type: %s (expected aws-key or gcp-service-account)", credType)
	}
}

func getCredential(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	tenant, err := getTenant(r)
	if err != nil {
		log.Warnf("Get: Invalid tenant for credential %s", name)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Debugf("Getting credential: %s/%s", tenant, name)

	resp, err := credentialClient.Get(tenant, name)
	if err != nil {
		log.Warnf("Get: Credential %s/%s failed: %s", tenant, name, err)
		http.Error(w, fmt.Sprintf("Getting credential failed: %s", err), http.StatusInternalServerError)
		return
	}
	if resp == nil {
		log.Debugf("Get: Credential %s/%s not found", tenant, name)
		http.Error(w, fmt.Sprintf("Credential not found: %s", name), http.StatusNotFound)
		return
	}

	encoder := yaml.NewEncoder(w)
	encoder.Encode(CredentialInfo{
		Name:      resp.CredentialByPk.Name,
		Type:      resp.CredentialByPk.Type,
		CreatedAt: resp.CredentialByPk.CreatedAt,
		UpdatedAt: resp.CredentialByPk.UpdatedAt,
	})
}

func deleteCredential(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	tenant, err := getTenant(r)
	if err != nil {
		log.Warnf("Delete: Invalid tenant for credential %s", name)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Debugf("Deleting credential: %s/%s", tenant, name)

	resp, err := credentialClient.Delete(tenant, name)
	if err != nil {
		log.Warnf("Delete: Credential %s/%s failed: %s", tenant, name, err)
		http.Error(w, fmt.Sprintf("Deleting credential failed: %s", err), http.StatusInternalServerError)
		return
	}
	if resp == nil {
		log.Debugf("Delete: Credential %s/%s not found", tenant, name)
		http.Error(w, fmt.Sprintf("Credential not found: %s/%s", tenant, name), http.StatusNotFound)
		return
	}

	encoder := yaml.NewEncoder(w)
	encoder.Encode(CredentialInfo{Name: resp.DeleteCredentialByPk.Name})
}

// Information about an exporter. Custom type which omits the tenant field.
type ExporterInfo struct {
	Name       string      `yaml:"name"`
	Type       string      `yaml:"type,omitempty"`
	Credential string      `yaml:"credential,omitempty"`
	Config     interface{} `yaml:"config,omitempty"`
	CreatedAt  string      `yaml:"created_at,omitempty"`
	UpdatedAt  string      `yaml:"updated_at,omitempty"`
}

func listExporters(w http.ResponseWriter, r *http.Request) {
	tenant, err := getTenant(r)
	if err != nil {
		log.Warn("List exporters: Invalid tenant")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp, err := exporterClient.List(tenant)
	if err != nil {
		log.Warnf("Listing exporters for tenant %s failed: %s", tenant, err)
		http.Error(w, fmt.Sprintf("Listing exporters for tenant %s failed: %s", tenant, err), http.StatusInternalServerError)
		return
	}

	log.Debugf("Listing %d exporters for tenant %s", len(resp.Exporter), tenant)

	encoder := yaml.NewEncoder(w)
	for _, exporter := range resp.Exporter {
		configJSON := make(map[string]interface{})
		err := json.Unmarshal([]byte(exporter.Config), &configJSON)
		if err != nil {
			// give up and pass-through the json
			log.Warnf("Failed to decode JSON config for exporter %s (err: %s): %s", exporter.Name, err, exporter.Config)
			configJSON["json"] = exporter.Config
		}
		encoder.Encode(ExporterInfo{
			Name:       exporter.Name,
			Type:       exporter.Type,
			Credential: exporter.Credential,
			Config:     configJSON,
			CreatedAt:  exporter.CreatedAt,
			UpdatedAt:  exporter.UpdatedAt,
		})
	}
}

// Exporter entry received from a POST request.
type Exporter struct {
	Name       string      `yaml:"name"`
	Type       string      `yaml:"type"`
	Credential string      `yaml:"credential,omitempty"`
	Config     interface{} `yaml:"config"` // nested yaml
}

func writeExporters(w http.ResponseWriter, r *http.Request) {
	tenant, err := getTenant(r)
	if err != nil {
		log.Warn("Write exporters: Invalid tenant")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	decoder := yaml.NewDecoder(r.Body)
	// Return error for unrecognized or duplicate fields in the input
	decoder.SetStrict(true)

	// Collect list of existing names so that we can decide between insert vs update
	existingTypes := make(map[string]string)
	resp, err := exporterClient.List(tenant)
	if err != nil {
		log.Warnf("Listing exporters failed for tenant %s: %s", tenant, err)
		http.Error(w, fmt.Sprintf("Listing exporters for tenant %s failed: %s", tenant, err), http.StatusInternalServerError)
		return
	}
	for _, exporter := range resp.Exporter {
		existingTypes[exporter.Name] = exporter.Type
	}

	now := nowTimestamp()

	var inserts []graphql.ExporterInsertInput
	var updates []graphql.UpdateExporterVariables
	for {
		var yamlExporter Exporter
		err := decoder.Decode(&yamlExporter)
		if err != nil {
			if err != io.EOF {
				log.Debugf("Decoding exporter input at index=%d failed: %s", len(inserts)+len(updates), err)
				http.Error(w, fmt.Sprintf(
					"Decoding exporter input at index=%d failed: %s", len(inserts)+len(updates), err,
				), http.StatusBadRequest)
				return
			}
			break
		}
		name := graphql.String(yamlExporter.Name)
		var credential *graphql.String
		if yamlExporter.Credential == "" {
			credential = nil
		} else {
			// TODO could validate that the referenced credential has the correct type for this exporter
			// for example, require that cloudwatch exporters are configured with aws-key credentials
			gcredential := graphql.String(yamlExporter.Credential)
			credential = &gcredential
		}

		var gconfig graphql.Json
		switch yamlMap := yamlExporter.Config.(type) {
		case map[interface{}]interface{}:
			// Encode the parsed YAML config tree as JSON
			convMap, err := recurseMapStringKeys(yamlMap)
			if err != nil {
				log.Debugf("Unable to serialize exporter '%s' config as JSON: %s", yamlExporter.Name, err)
				http.Error(w, fmt.Sprintf(
					"Exporter '%s' config could not be encoded as JSON: %s", yamlExporter.Name, err,
				), http.StatusBadRequest)
				return
			}
			json, err := json.Marshal(convMap)
			if err != nil {
				log.Debugf("Unable to serialize exporter '%s' config as JSON: %s", yamlExporter.Name, err)
				http.Error(w, fmt.Sprintf(
					"Exporter '%s' config could not be encoded as JSON: %s", yamlExporter.Name, err,
				), http.StatusBadRequest)
				return
			}
			gconfig = graphql.Json(json)
		default:
			log.Debugf("Invalid exporter '%s' config type", yamlExporter.Name)
			http.Error(w, fmt.Sprintf(
				"Exporter '%s' config is invalid (must be YAML map)", yamlExporter.Name,
			), http.StatusBadRequest)
			return
		}

		if existingType, ok := existingTypes[yamlExporter.Name]; ok {
			// Explicitly check and complain if the user tries to change the exporter type
			if yamlExporter.Type != "" && existingType != yamlExporter.Type {
				log.Debugf("Invalid exporter '%s' type change", yamlExporter.Name)
				http.Error(w, fmt.Sprintf(
					"Exporter '%s' type cannot be updated (current=%s, updated=%s)",
					yamlExporter.Name, existingType, yamlExporter.Type,
				), http.StatusBadRequest)
				return
			}
			// TODO check for no-op updates and skip them (and avoid unnecessary changes to UpdatedAt)
			updates = append(updates, graphql.UpdateExporterVariables{
				Name:       name,
				Credential: credential,
				Config:     gconfig,
				UpdatedAt:  now,
			})
		} else {
			expType := graphql.String(yamlExporter.Type)
			inserts = append(inserts, graphql.ExporterInsertInput{
				Name:       &name,
				Type:       &expType,
				Credential: credential,
				Config:     &gconfig,
				CreatedAt:  &now,
				UpdatedAt:  &now,
			})
		}
	}

	if len(inserts)+len(updates) == 0 {
		log.Debugf("Writing exporters: No data provided")
		http.Error(w, "Missing exporter YAML data in request body", http.StatusBadRequest)
		return
	}

	log.Debugf("Writing exporters: %d insert, %d update", len(inserts), len(updates))

	if len(inserts) != 0 {
		err := exporterClient.Insert(tenant, inserts)
		if err != nil {
			log.Warnf("Insert: %d exporters failed: %s", len(inserts), err)
			http.Error(w, fmt.Sprintf("Creating %d exporters failed: %s", len(inserts), err), http.StatusInternalServerError)
			return
		}
	}
	if len(updates) != 0 {
		for _, update := range updates {
			err := exporterClient.Update(tenant, update)
			if err != nil {
				log.Warnf("Update: Exporter %s failed: %s", update.Name, err)
				http.Error(w, fmt.Sprintf("Updating exporter %s failed: %s", update.Name, err), http.StatusInternalServerError)
				return
			}
		}
	}
}

// Searches through the provided object tree for any maps with interface keys (from YAML),
// and converts those keys to strings (required for JSON).
func recurseMapStringKeys(in interface{}) (interface{}, error) {
	switch inType := in.(type) {
	case map[interface{}]interface{}: // yaml type for maps. needs string keys to work with JSON
		// Ensure the map keys are converted, RECURSE into values to find nested maps
		strMap := make(map[string]interface{})
		for k, v := range inType {
			switch kType := k.(type) {
			case string:
				conv, err := recurseMapStringKeys(v)
				if err != nil {
					return nil, err
				}
				strMap[kType] = conv
			default:
				return nil, errors.New("map is invalid (keys must be strings)")
			}
		}
		return strMap, nil
	case []interface{}:
		// RECURSE into entries to convert any nested maps are converted
		for i, v := range inType {
			conv, err := recurseMapStringKeys(v)
			if err != nil {
				return nil, err
			}
			inType[i] = conv
		}
		return inType, nil
	default:
		return in, nil
	}
}

func getExporter(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	tenant, err := getTenant(r)
	if err != nil {
		log.Warnf("Get: Invalid tenant for exporter %s", name)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Debugf("Getting exporter: %s/%s", tenant, name)

	resp, err := exporterClient.Get(tenant, name)
	if err != nil {
		log.Warnf("Get: Exporter %s/%s failed: %s", tenant, name, err)
		http.Error(w, fmt.Sprintf("Getting exporter failed: %s", err), http.StatusInternalServerError)
		return
	}
	if resp == nil {
		log.Debugf("Get: Exporter %s/%s not found", tenant, name)
		http.Error(w, fmt.Sprintf("Exporter not found: %s/%s", tenant, name), http.StatusNotFound)
		return
	}

	configJSON := make(map[string]interface{})
	err = json.Unmarshal([]byte(resp.ExporterByPk.Config), &configJSON)
	if err != nil {
		// give up and pass-through the json
		log.Warnf(
			"Failed to decode JSON config for exporter %s (err: %s): %s",
			resp.ExporterByPk.Name, err, resp.ExporterByPk.Config,
		)
		configJSON["json"] = resp.ExporterByPk.Config
	}

	encoder := yaml.NewEncoder(w)
	encoder.Encode(ExporterInfo{
		Name:       resp.ExporterByPk.Name,
		Type:       resp.ExporterByPk.Type,
		Credential: resp.ExporterByPk.Credential,
		Config:     configJSON,
		CreatedAt:  resp.ExporterByPk.CreatedAt,
		UpdatedAt:  resp.ExporterByPk.UpdatedAt,
	})
}

func deleteExporter(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	tenant, err := getTenant(r)
	if err != nil {
		log.Warnf("Delete: Invalid tenant for exporter %s", name)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Debugf("Deleting exporter: %s/%s", tenant, name)

	resp, err := exporterClient.Delete(tenant, name)
	if err != nil {
		log.Warnf("Delete: Exporter %s/%s failed: %s", tenant, name, err)
		http.Error(w, fmt.Sprintf("Deleting exporter failed: %s", err), http.StatusInternalServerError)
		return
	}
	if resp == nil {
		log.Debugf("Delete: Exporter %s/%s not found", tenant, name)
		http.Error(w, fmt.Sprintf("Exporter not found: %s/%s", tenant, name), http.StatusNotFound)
		return
	}

	encoder := yaml.NewEncoder(w)
	encoder.Encode(ExporterInfo{Name: resp.DeleteExporterByPk.Name})
}

func getTenant(r *http.Request) (string, error) {
	tenants, ok := r.Header["X-Scope-Orgid"]
	if !ok {
		return "", fmt.Errorf("missing tenant ID in request to %s", r.URL)
	}
	if len(tenants) != 1 || len(tenants[0]) == 0 {
		return "", fmt.Errorf("invalid tenant ID in request to %s", r.URL)
	}
	return tenants[0], nil
}

// Returns a string representation of the current time in UTC, suitable for passing to Hasura as a timestamptz
// See also https://hasura.io/blog/postgres-date-time-data-types-on-graphql-fd926e86ee87/
func nowTimestamp() graphql.Timestamptz {
	return graphql.Timestamptz(time.Now().Format(time.RFC3339))
}