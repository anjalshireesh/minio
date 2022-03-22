// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"github.com/mattn/go-ieproxy"
	"github.com/minio/madmin-go/support"
	"github.com/minio/minio/internal/config"
)

const (
	subnetRespBodyLimit = 1 << 20 // 1 MiB
)

func subnetAuthHeaders(authToken string) map[string]string {
	return map[string]string{"Authorization": authToken}
}

func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: ieproxy.GetProxyFunc(),
			TLSClientConfig: &tls.Config{
				RootCAs: globalRootCAs,
				// Can't use SSLv3 because of POODLE and BEAST
				// Can't use TLSv1.0 because of POODLE and BEAST using CBC cipher
				// Can't use TLSv1.1 because of RC4 cipher usage
				MinVersion: tls.VersionTLS12,
			},
		},
	}
}

func getSubnetConfig(key string) (val string, found bool) {
	tgts, _ := globalServerConfig.GetKVS(config.SubnetSubSys, config.DefaultKVS)
	for _, tgt := range tgts {
		val, found = tgt.KVS.Lookup(key)
		if found {
			return val, found
		}
	}
	return "", false
}

func httpDo(req *http.Request) (*http.Response, error) {
	proxy, proxySet := getSubnetConfig("proxy")
	client := httpClient(10 * time.Second)
	if proxySet && len(proxy) > 0 {
		proxyURL, e := url.Parse(proxy)
		if e != nil {
			return nil, e
		}
		client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyURL)
	}
	return client.Do(req)
}

func subnetReqDo(r *http.Request, headers map[string]string) (string, error) {
	for k, v := range headers {
		r.Header.Add(k, v)
	}

	ct := r.Header.Get("Content-Type")
	if len(ct) == 0 {
		r.Header.Add("Content-Type", "application/json")
	}

	resp, e := httpDo(r)
	if e != nil {
		return "", e
	}

	defer resp.Body.Close()
	respBytes, e := ioutil.ReadAll(io.LimitReader(resp.Body, subnetRespBodyLimit))
	if e != nil {
		return "", e
	}
	respStr := string(respBytes)

	if resp.StatusCode == http.StatusOK {
		return respStr, nil
	}
	return respStr, fmt.Errorf("Request failed with code %d and error: %s", resp.StatusCode, respStr)
}

func subnetGetReq(reqURL string, headers map[string]string) (string, error) {
	r, e := http.NewRequest(http.MethodGet, reqURL, nil)
	if e != nil {
		return "", e
	}
	return subnetReqDo(r, headers)
}

func subnetPostReq(reqURL string, payload interface{}, headers map[string]string) (string, error) {
	body, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	r, e := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(body))
	if e != nil {
		return "", e
	}
	return subnetReqDo(r, headers)
}

func subnetBaseURL() string {
	if globalIsCICD {
		return "http://localhost:9000"
	}

	return "https://subnet.min.io"
}

func subnetCallhomeURL() string {
	return subnetBaseURL() + "/api/support/metrics"
}

func sendCallhomeMetrics(metrics support.Metrics) (string, error) {
	apiKey, found := getSubnetConfig("api_key")
	if !found {
		return "", errors.New("Cluster is not registered with SUBNET.")
	}
	headers := subnetAuthHeaders(apiKey)
	return subnetPostReq(subnetCallhomeURL(), metrics, headers)
}
