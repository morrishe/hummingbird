//  Copyright (c) 2018 Rackspace
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
//  implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// To use with keystone, create s3 style credentials
//   `openstack ec2 credentials create`
//
// To enable, add the following to `/etc/hummingbird/proxy-server.conf`
//
//   [filter:s3api]
//   enabled = true
//
// Example using boto2 and haio with tempauth:
//
//  from boto.s3.connection import S3Connection
//  connection = S3Connection(
//    aws_access_key_id="test:tester",
//    aws_secret_access_key="testing",
//    port=8080,
//    host='127.0.0.1',
//    is_secure=False,
//    calling_format=boto.s3.connection.OrdinaryCallingFormat()
//  )
//  connection.get_all_buckets()
//
//  If you are using keystone auth, substitue the key and access key returned from keystone

package middleware

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/troubling/hummingbird/accountserver"
	"github.com/troubling/hummingbird/common/conf"
	"github.com/troubling/hummingbird/common/srv"
	"github.com/troubling/hummingbird/containerserver"
	"github.com/uber-go/tally"
)

const (
	s3Xmlns = "http://s3.amazonaws.com/doc/2006-03-01"
)

type s3Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type s3BucketInfo struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type s3BucketList struct {
	XMLName xml.Name       `xml:"ListAllMyBucketsResult"`
	Xmlns   string         `xml:"xmlns,attr"`
	Owner   s3Owner        `xml:"Owner"`
	Buckets []s3BucketInfo `xml:"Buckets>Bucket"`
}

func NewS3BucketList() *s3BucketList {
	return &s3BucketList{Xmlns: s3Xmlns}
}

type s3ObjectInfo struct {
	Name         string   `xml:"Key"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml"ETag"`
	Size         int64    `xml"Size"`
	StorageClass string   `xml"StorageClass"`
	Owner        *s3Owner `xml"Owner,omitempty"`
}

type s3ObjectList struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Xmlns                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	Marker                string         `xml:"Marker"`
	NextMarker            string         `xml:"NextMarker"`
	MaxKeys               int            `xml:"MaxKeys"`
	IsTruncated           bool           `xml:"IsTruncated"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinationToken,omitempty"`
	StartAfter            string         `xml:"StartAfter,omitempty"`
	KeyCount              string         `xml:"KeyCount,omitempty"`
	Objects               []s3ObjectInfo `xml:"Contents"`
}

func NewS3ObjectList() *s3ObjectList {
	return &s3ObjectList{Xmlns: s3Xmlns}
}

type s3Error struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource"`
	RequestId string   `xml:"RequestId"`
}

func NewS3Error() *s3Error {
	return &s3Error{}
}

// TODO: Figure out how to plumb S3 Responses
func S3ErrorResponse(w http.ResponseWriter, statusCode int, resource, requestId string) {
	s3err := NewS3Error()
	// TODO: Add all error codes
	if statusCode == 403 {
		statusCode = 403
		s3err.Code = "AccessDenied"
		s3err.Message = "Access Denied"
	}
	s3err.Resource = resource
	s3err.RequestId = requestId
	output, err := xml.MarshalIndent(s3err, "", "  ")
	if err != nil {
		srv.SimpleErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	output = []byte(xml.Header + string(output))
	headers := w.Header()
	headers.Set("Content-Type", "application/xml; charset=utf-8")
	headers.Set("Content-Length", strconv.Itoa(len(output)))
	w.WriteHeader(statusCode)
	w.Write(output)

}

type s3ApiHandler struct {
	next           http.Handler
	ctx            *ProxyContext
	account        string
	container      string
	object         string
	path           string
	signature      string
	requestsMetric tally.Counter
}

func (s *s3ApiHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	ctx := GetProxyContext(request)
	// Check if this is an S3 request
	if ctx.S3Auth == nil || strings.HasPrefix(strings.ToLower(request.URL.Path), "/v1/") {
		// Not an S3 request
		s.next.ServeHTTP(writer, request)
		return
	}

	s.container, s.object, _, _ = getPathSegments(request.URL.Path)
	s.account = ctx.S3Auth.Account

	// TODO: Validate the container
	// Generate the hbird api path
	if s.object != "" {
		s.path = fmt.Sprintf("/v1/AUTH_%s/%s/%s", s.account, s.container, s.object)
	} else if s.container != "" {
		s.path = fmt.Sprintf("/v1/AUTH_%s/%s", s.account, s.container)
	} else {
		s.path = fmt.Sprintf("/v1/AUTH_%s", s.account)
	}
	// TODO: Handle metadata?

	if s.object != "" {
		s.handleObjectRequest(writer, request)
		return
	} else if s.container != "" {
		s.handleContainerRequest(writer, request)
		return
	} else {
		s.handleAccountRequest(writer, request)
		return
	}
}

func (s *s3ApiHandler) handleObjectRequest(writer http.ResponseWriter, request *http.Request) {
	// If we didn't get to anything, then return no implemented
	srv.SimpleErrorResponse(writer, http.StatusNotImplemented, "Not Implemented")
}

func (s *s3ApiHandler) handleContainerRequest(writer http.ResponseWriter, request *http.Request) {
	ctx := GetProxyContext(request)

	if request.Method == "HEAD" {
		newReq, err := ctx.newSubrequest("HEAD", s.path, http.NoBody, request, "s3api")
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
			return
		}
		cap := NewCaptureWriter()
		ctx.serveHTTPSubrequest(cap, newReq)
		if cap.status/100 != 2 {
			srv.StandardResponse(writer, cap.status)
			return
		} else {
			writer.WriteHeader(200)
			return
		}
	}

	if request.Method == "DELETE" {
		newReq, err := ctx.newSubrequest("DELETE", s.path, http.NoBody, request, "s3api")
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
		}
		cap := NewCaptureWriter()
		ctx.serveHTTPSubrequest(cap, newReq)
		if cap.status/100 != 2 {
			srv.StandardResponse(writer, cap.status)
			return
		} else {
			writer.WriteHeader(204)
			return
		}
	}

	if request.Method == "PUT" {
		newReq, err := ctx.newSubrequest("PUT", s.path, http.NoBody, request, "s3api")
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
		}
		cap := NewCaptureWriter()
		ctx.serveHTTPSubrequest(cap, newReq)
		if cap.status/100 != 2 {
			srv.StandardResponse(writer, cap.status)
			return
		} else {
			writer.WriteHeader(200)
			return
		}
	}

	if request.Method == "GET" {
		maxKeys, err := strconv.Atoi(request.Form.Get("max-keys"))
		if err != nil {
			maxKeys = 1000
		}
		newReq, err := ctx.newSubrequest("GET", s.path, http.NoBody, request, "s3api")
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
			return
		}
		newReq.Header.Set("Accept", "application/json")
		nq := newReq.URL.Query()
		q := request.URL.Query()
		nq.Set("limit", strconv.Itoa(maxKeys+1))
		ver := q.Get("list-type")
		marker := q.Get("marker")
		prefix := q.Get("prefix")
		delimeter := q.Get("delimeter")
		fetchOwner := false
		if ver == "2" {
			marker = q.Get("start-after")
			cont := q.Get("continuation-token")
			if cont != "" {
				if b, err := base64.StdEncoding.DecodeString(cont); err == nil {
					marker = string(b)
				}
			}
			fetchOwner, err = strconv.ParseBool(q.Get("fetch-owner"))
			if err != nil {
				fetchOwner = false
			}
		}
		if marker != "" {
			nq.Set("marker", marker)
		}
		if prefix != "" {
			nq.Set("prefix", prefix)
		}
		if delimeter != "" {
			nq.Set("delimiter", delimeter)
		}
		cap := NewCaptureWriter()
		newReq.URL.RawQuery = nq.Encode()
		ctx.serveHTTPSubrequest(cap, newReq)
		if cap.status/100 != 2 {
			srv.StandardResponse(writer, cap.status)
			return
		}
		objectListing := []containerserver.ObjectListingRecord{}
		err = json.Unmarshal(cap.body, &objectListing)
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
			return
		}
		truncated := maxKeys > 0 && len(objectListing) > maxKeys
		if len(objectListing) > maxKeys {
			objectListing = objectListing[:maxKeys]
		}
		objectList := NewS3ObjectList()
		objectList.Name = s.container
		objectList.MaxKeys = maxKeys
		objectList.IsTruncated = truncated
		objectList.Marker = marker
		objectList.Prefix = prefix
		if ver == "2" {
			if truncated {
				objectList.NextContinuationToken = base64.StdEncoding.EncodeToString([]byte(objectListing[len(objectListing)].Name))
			}
			objectList.ContinuationToken = q.Get("continuation-token")
			objectList.StartAfter = q.Get("start-after")
			objectList.KeyCount = strconv.Itoa(len(objectListing))
		} else {
			if truncated && delimeter != "" {
				objectList.NextMarker = objectListing[len(objectListing)].Name
			}
		}
		for _, o := range objectListing {
			obj := s3ObjectInfo{
				Name:         o.Name,
				LastModified: o.LastModified + "Z",
				ETag:         "\"" + o.ETag + "\"",
				Size:         o.Size,
				StorageClass: "STANDARD",
			}
			if fetchOwner || ver != "2" {
				obj.Owner = &s3Owner{
					ID:          ctx.S3Auth.Account,
					DisplayName: ctx.S3Auth.Account,
				}
			}
			objectList.Objects = append(objectList.Objects, obj)
		}
		output, err := xml.MarshalIndent(objectList, "", "  ")
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
			return
		}
		output = []byte(xml.Header + string(output))
		headers := writer.Header()
		headers.Set("Content-Type", "application/xml; charset=utf-8")
		headers.Set("Content-Length", strconv.Itoa(len(output)))
		writer.WriteHeader(200)
		writer.Write(output)
		return

	}
	// If we didn't get to anything, then return no implemented
	srv.SimpleErrorResponse(writer, http.StatusNotImplemented, "Not Implemented")
}

func (s *s3ApiHandler) handleAccountRequest(writer http.ResponseWriter, request *http.Request) {
	ctx := GetProxyContext(request)
	if request.Method == "GET" {
		newReq, err := ctx.newSubrequest("GET", s.path, http.NoBody, request, "s3api")
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
			return
		}
		newReq.Header.Set("Accept", "application/json")
		cap := NewCaptureWriter()
		ctx.serveHTTPSubrequest(cap, newReq)
		if cap.status/100 != 2 {
			srv.StandardResponse(writer, cap.status)
			return
		}
		containerListing := []accountserver.ContainerListingRecord{}
		err = json.Unmarshal(cap.body, &containerListing)
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
			return
		}
		bucketList := NewS3BucketList()
		bucketList.Owner.ID = ctx.S3Auth.Account
		bucketList.Owner.DisplayName = ctx.S3Auth.Account
		// NOTE: The container list api doesn't have a creation date for the container, so we use an "arbitrary" date.
		for _, c := range containerListing {
			bucketList.Buckets = append(bucketList.Buckets, s3BucketInfo{
				Name:         c.Name,
				CreationDate: "2009-02-03T16:45:09.000Z",
			})
		}
		output, err := xml.MarshalIndent(bucketList, "", "  ")
		if err != nil {
			srv.SimpleErrorResponse(writer, http.StatusInternalServerError, err.Error())
			return
		}
		output = []byte(xml.Header + string(output))
		headers := writer.Header()
		headers.Set("Content-Type", "application/xml; charset=utf-8")
		headers.Set("Content-Length", strconv.Itoa(len(output)))
		writer.WriteHeader(200)
		writer.Write(output)
		return

	}

	// No other methods are allowed at the account level
	srv.StandardResponse(writer, http.StatusMethodNotAllowed)
}

func NewS3Api(config conf.Section, metricsScope tally.Scope) (func(http.Handler) http.Handler, error) {
	enabled, ok := config.Section["enabled"]
	if !ok || strings.Compare(strings.ToLower(enabled), "false") == 0 {
		// s3api is disabled, so pass the request on
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				next.ServeHTTP(writer, request)
			})
		}, nil
	}
	RegisterInfo("s3Api", map[string]interface{}{})
	return s3Api(metricsScope.Counter("s3Api_requests")), nil
}

func s3Api(requestsMetric tally.Counter) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			(&s3ApiHandler{next: next, requestsMetric: requestsMetric}).ServeHTTP(writer, request)
		})
	}
}
