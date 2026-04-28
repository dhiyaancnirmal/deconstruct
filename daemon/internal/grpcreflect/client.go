package grpcreflect

import (
	"context"
	"crypto/tls"
	"fmt"
	"sort"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
)

type Params struct {
	Target    string `json:"target"`
	Plaintext bool   `json:"plaintext,omitempty"`
}

type Service struct {
	Name string `json:"name"`
}

type Result struct {
	Target   string    `json:"target"`
	Services []Service `json:"services"`
}

func ListServices(ctx context.Context, params Params) (Result, error) {
	if params.Target == "" {
		return Result{}, fmt.Errorf("target is required")
	}
	var opts []grpc.DialOption
	if params.Plaintext {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	}
	conn, err := grpc.DialContext(ctx, params.Target, opts...)
	if err != nil {
		return Result{}, fmt.Errorf("dial grpc target: %w", err)
	}
	defer conn.Close()
	client := reflectionpb.NewServerReflectionClient(conn)
	stream, err := client.ServerReflectionInfo(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("open reflection stream: %w", err)
	}
	if err := stream.Send(&reflectionpb.ServerReflectionRequest{
		Host:           params.Target,
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{},
	}); err != nil {
		return Result{}, fmt.Errorf("send reflection request: %w", err)
	}
	response, err := stream.Recv()
	if err != nil {
		return Result{}, fmt.Errorf("receive reflection response: %w", err)
	}
	list := response.GetListServicesResponse()
	if list == nil {
		if errResp := response.GetErrorResponse(); errResp != nil {
			return Result{}, fmt.Errorf("reflection error %d: %s", errResp.ErrorCode, errResp.ErrorMessage)
		}
		return Result{}, fmt.Errorf("reflection response did not include service list")
	}
	result := Result{Target: params.Target}
	for _, service := range list.Service {
		result.Services = append(result.Services, Service{Name: service.Name})
	}
	sort.Slice(result.Services, func(i, j int) bool { return result.Services[i].Name < result.Services[j].Name })
	return result, nil
}
