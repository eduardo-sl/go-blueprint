package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	customerv1 "github.com/eduardo-sl/go-blueprint/gen/customer/v1"
	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	grpcserver "github.com/eduardo-sl/go-blueprint/internal/platform/grpc"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"log/slog"
	"io"
)

const _bufSize = 1024 * 1024

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T, authSvc *auth.Service) (customerv1.CustomerServiceClient, func()) {
	t.Helper()

	handler := customer.NewGRPCHandler(
		customer.NewService(
			&noopRepo{}, &noopBeginner{}, &noopOutboxStore{}, &noopEventStore{},
			cache.NoopCache{}, discardLogger(),
		),
		customer.NewQueryService(&noopRepo{}),
		&noopEventStore{},
	)

	srv := grpcserver.NewServer(handler, authSvc, discardLogger())

	lis := bufconn.Listen(_bufSize)
	t.Cleanup(func() { srv.Stop() })

	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	return customerv1.NewCustomerServiceClient(conn), func() { _ = conn.Close() }
}

func newAuthSvc(t *testing.T) *auth.Service {
	t.Helper()
	return auth.NewService(&noopAuthRepo{}, "test-secret-32-chars-long-enough!", 24*time.Hour, discardLogger())
}

func issueToken(t *testing.T, svc *auth.Service) string {
	t.Helper()
	ctx := context.Background()
	_, _ = svc.Register(ctx, auth.RegisterCmd{Email: "test@example.com", Name: "Test", Password: "password123"})
	resp, err := svc.Login(ctx, auth.LoginCmd{Email: "test@example.com", Password: "password123"})
	require.NoError(t, err)
	return resp.Token
}

func TestAuthInterceptor_MissingToken(t *testing.T) {
	authSvc := newAuthSvc(t)
	client, cleanup := newTestServer(t, authSvc)
	defer cleanup()

	_, err := client.ListCustomers(context.Background(), &customerv1.ListCustomersRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthInterceptor_InvalidToken(t *testing.T) {
	authSvc := newAuthSvc(t)
	client, cleanup := newTestServer(t, authSvc)
	defer cleanup()

	md := metadata.Pairs("authorization", "Bearer invalid.token.here")
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	_, err := client.ListCustomers(ctx, &customerv1.ListCustomersRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthInterceptor_ValidToken(t *testing.T) {
	authSvc := newAuthSvc(t)
	client, cleanup := newTestServer(t, authSvc)
	defer cleanup()

	token := issueToken(t, authSvc)
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	resp, err := client.ListCustomers(ctx, &customerv1.ListCustomersRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Customers)
}
