package grpc

import (
	"context"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// recoveryInterceptor catches panics in unary handlers and converts them to
// codes.Internal so a single bad request cannot bring down the server.
func recoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "grpc panic recovered",
					slog.String("method", info.FullMethod),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// streamRecoveryInterceptor is the streaming equivalent of recoveryInterceptor.
func streamRecoveryInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ss.Context(), "grpc stream panic recovered",
					slog.String("method", info.FullMethod),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, ss)
	}
}

// loggingInterceptor logs method, status code, and duration for every unary call.
func loggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.InfoContext(ctx, "grpc request",
			slog.String("method", info.FullMethod),
			slog.String("code", status.Code(err).String()),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
		return resp, err
	}
}

// streamLoggingInterceptor logs method, status code, and duration for streaming calls.
func streamLoggingInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logger.InfoContext(ss.Context(), "grpc stream",
			slog.String("method", info.FullMethod),
			slog.String("code", status.Code(err).String()),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
		return err
	}
}

// authInterceptor validates the Bearer token from gRPC metadata and injects the
// JWT claims into the context under auth.ClaimsKey.
func authInterceptor(authSvc *auth.Service) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		token, err := extractToken(ctx)
		if err != nil {
			return nil, err
		}
		claims, err := authSvc.ValidateToken(token)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid token")
		}
		ctx = context.WithValue(ctx, auth.ClaimsKey, claims)
		return handler(ctx, req)
	}
}

// streamAuthInterceptor is the streaming equivalent of authInterceptor.
func streamAuthInterceptor(authSvc *auth.Service) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		token, err := extractToken(ss.Context())
		if err != nil {
			return err
		}
		claims, err := authSvc.ValidateToken(token)
		if err != nil {
			return status.Error(codes.Unauthenticated, "invalid token")
		}
		ctx := context.WithValue(ss.Context(), auth.ClaimsKey, claims)
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}
}

func extractToken(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization")
	}
	return strings.TrimPrefix(values[0], "Bearer "), nil
}

// wrappedStream overrides the context on a grpc.ServerStream.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }
