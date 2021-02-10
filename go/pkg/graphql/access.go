// Copyright 2019-2021 Opstrace, Inc.
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

package graphql

import (
	"encoding/json"
	"net/http"
	"net/url"
)

type graphqlAccess struct {
	URL    string
	client *http.Client
	secret string
}

func newGraphqlAccess(url *url.URL, secret string) *graphqlAccess {
	return &graphqlAccess{
		url.String(),
		&http.Client{},
		secret,
	}
}

// Execute adds the secret onto the request, executes it, and deserializes the response into `result`.
func (g *graphqlAccess) Execute(req *http.Request, result interface{}) error {
	if g.secret != "" {
		req.Header.Add("x-hasura-admin-secret", g.secret)
	}

	resp, err := execute(g.client, req)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(resp.Data, result); err != nil {
		return err
	}
	return nil
}