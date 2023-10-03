// Copyright 2021-2023 The Connect Authors
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

// Code generated by protoc-gen-connect-go. DO NOT EDIT.
//
// Source: connect/collide/v1/collide.proto

package collidev1connect

import (
	connect "connectrpc.com/connect"
	v1 "connectrpc.com/connect/internal/gen/connect/collide/v1"
	context "context"
	errors "errors"
	http "net/http"
	strings "strings"
)

// This is a compile-time assertion to ensure that this generated file and the connect package are
// compatible. If you get a compiler error that this constant is not defined, this code was
// generated with a version of connect newer than the one compiled into your binary. You can fix the
// problem by either regenerating this code with an older version of connect or updating the connect
// version compiled into your binary.
const _ = connect.IsAtLeastVersion0_1_0

const (
	// CollideServiceName is the fully-qualified name of the CollideService service.
	CollideServiceName = "connect.collide.v1.CollideService"
)

// These constants are the fully-qualified names of the RPCs defined in this package. They're
// exposed at runtime as Spec.Procedure and as the final two segments of the HTTP route.
//
// Note that these are different from the fully-qualified method names used by
// google.golang.org/protobuf/reflect/protoreflect. To convert from these constants to
// reflection-formatted method names, remove the leading slash and convert the remaining slash to a
// period.
const (
	// CollideServiceImportProcedure is the fully-qualified name of the CollideService's Import RPC.
	CollideServiceImportProcedure = "/connect.collide.v1.CollideService/Import"
)

// CollideServiceClient is a client for the connect.collide.v1.CollideService service.
type CollideServiceClient interface {
	Import(context.Context, *connect.Request[v1.ImportRequest]) (*connect.Response[v1.ImportResponse], error)
}

// NewCollideServiceClient constructs a client for the connect.collide.v1.CollideService service. By
// default, it uses the Connect protocol with the binary Protobuf Codec, asks for gzipped responses,
// and sends uncompressed requests. To use the gRPC or gRPC-Web protocols, supply the
// connect.WithGRPC() or connect.WithGRPCWeb() options.
//
// The URL supplied here should be the base URL for the Connect or gRPC server (for example,
// http://api.acme.com or https://acme.com/grpc).
func NewCollideServiceClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) CollideServiceClient {
	baseURL = strings.TrimRight(baseURL, "/")
	return &collideServiceClient{
		_import: connect.NewClient[v1.ImportRequest, v1.ImportResponse](
			httpClient,
			baseURL+CollideServiceImportProcedure,
			opts...,
		),
	}
}

// collideServiceClient implements CollideServiceClient.
type collideServiceClient struct {
	_import *connect.Client[v1.ImportRequest, v1.ImportResponse]
}

// Import calls connect.collide.v1.CollideService.Import.
func (c *collideServiceClient) Import(ctx context.Context, req *connect.Request[v1.ImportRequest]) (*connect.Response[v1.ImportResponse], error) {
	return c._import.CallUnary(ctx, req)
}

// CollideServiceHandler is an implementation of the connect.collide.v1.CollideService service.
type CollideServiceHandler interface {
	Import(context.Context, *connect.Request[v1.ImportRequest]) (*connect.Response[v1.ImportResponse], error)
}

// NewCollideServiceHandler builds an HTTP handler from the service implementation. It returns the
// path on which to mount the handler and the handler itself.
//
// By default, handlers support the Connect, gRPC, and gRPC-Web protocols with the binary Protobuf
// and JSON codecs. They also support gzip compression.
func NewCollideServiceHandler(svc CollideServiceHandler, opts ...connect.HandlerOption) (string, http.Handler) {
	collideServiceImportHandler := connect.NewUnaryHandler(
		CollideServiceImportProcedure,
		svc.Import,
		opts...,
	)
	return "/connect.collide.v1.CollideService/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case CollideServiceImportProcedure:
			collideServiceImportHandler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// UnimplementedCollideServiceHandler returns CodeUnimplemented from all methods.
type UnimplementedCollideServiceHandler struct{}

func (UnimplementedCollideServiceHandler) Import(context.Context, *connect.Request[v1.ImportRequest]) (*connect.Response[v1.ImportResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("connect.collide.v1.CollideService.Import is not implemented"))
}
