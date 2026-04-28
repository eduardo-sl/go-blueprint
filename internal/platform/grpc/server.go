package grpc

import (
	"log/slog"

	customerv1 "github.com/eduardo-sl/go-blueprint/gen/customer/v1"
	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// NewServer wires a gRPC server with recovery, logging, and auth interceptors.
// reflection.Register is enabled so grpcurl and Postman can discover services
// without the proto file.
func NewServer(handler *customer.GRPCHandler, authSvc *auth.Service, logger *slog.Logger) *grpc.Server {
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			recoveryInterceptor(logger),
			loggingInterceptor(logger),
			authInterceptor(authSvc),
		),
		grpc.ChainStreamInterceptor(
			streamRecoveryInterceptor(logger),
			streamLoggingInterceptor(logger),
			streamAuthInterceptor(authSvc),
		),
	)

	customerv1.RegisterCustomerServiceServer(srv, handler)
	reflection.Register(srv)

	return srv
}
