package customer_test

import (
	"context"
	"net"
	"testing"
	"time"

	customerv1 "github.com/eduardo-sl/go-blueprint/gen/customer/v1"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const _bufSize = 1024 * 1024

// newTestGRPCClient spins up an in-process gRPC server with bufconn (no real TCP port).
func newTestGRPCClient(t *testing.T, handler *customer.GRPCHandler) customerv1.CustomerServiceClient {
	t.Helper()

	lis := bufconn.Listen(_bufSize)
	srv := grpc.NewServer()
	customerv1.RegisterCustomerServiceServer(srv, handler)
	t.Cleanup(func() { srv.Stop() })

	go func() {
		_ = srv.Serve(lis)
	}()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return customerv1.NewCustomerServiceClient(conn)
}

func newGRPCHandler(repo *mockRepo) *customer.GRPCHandler {
	return customer.NewGRPCHandler(newTestService(repo), customer.NewQueryService(repo), &noopEventStore{})
}

func TestGRPCHandler_RegisterCustomer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      *customerv1.RegisterCustomerRequest
		wantCode codes.Code
	}{
		{
			name:     "valid",
			req:      &customerv1.RegisterCustomerRequest{Name: "Alice", Email: "alice@example.com", BirthDate: "1990-01-15"},
			wantCode: codes.OK,
		},
		{
			name:     "malformed birth_date",
			req:      &customerv1.RegisterCustomerRequest{Name: "Bob", Email: "bob@example.com", BirthDate: "not-a-date"},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "future birth_date",
			req:      &customerv1.RegisterCustomerRequest{Name: "Carol", Email: "carol@example.com", BirthDate: "2099-01-01"},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := newTestGRPCClient(t, newGRPCHandler(newMockRepo()))

			resp, err := client.RegisterCustomer(context.Background(), tc.req)
			if tc.wantCode == codes.OK {
				require.NoError(t, err)
				assert.NotEmpty(t, resp.Id)
				_, parseErr := uuid.Parse(resp.Id)
				assert.NoError(t, parseErr)
			} else {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, status.Code(err))
			}
		})
	}
}

func TestGRPCHandler_RegisterCustomer_DuplicateEmail(t *testing.T) {
	t.Parallel()

	repo := newMockRepo()
	client := newTestGRPCClient(t, newGRPCHandler(repo))
	req := &customerv1.RegisterCustomerRequest{Name: "Alice", Email: "dup@example.com", BirthDate: "1990-01-01"}

	_, err := client.RegisterCustomer(context.Background(), req)
	require.NoError(t, err)

	_, err = client.RegisterCustomer(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
}

func TestGRPCHandler_GetCustomer(t *testing.T) {
	t.Parallel()

	repo := newMockRepo()
	c, err := customer.New("Alice", "alice@example.com", time.Now().AddDate(-1, 0, 0))
	require.NoError(t, err)
	require.NoError(t, repo.Save(context.Background(), c))

	client := newTestGRPCClient(t, newGRPCHandler(repo))

	t.Run("found", func(t *testing.T) {
		resp, err := client.GetCustomer(context.Background(), &customerv1.GetCustomerRequest{Id: c.ID.String()})
		require.NoError(t, err)
		assert.Equal(t, c.ID.String(), resp.Customer.Id)
		assert.Equal(t, "Alice", resp.Customer.Name)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := client.GetCustomer(context.Background(), &customerv1.GetCustomerRequest{Id: uuid.New().String()})
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("invalid uuid", func(t *testing.T) {
		_, err := client.GetCustomer(context.Background(), &customerv1.GetCustomerRequest{Id: "bad-uuid"})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestGRPCHandler_ListCustomers(t *testing.T) {
	t.Parallel()

	repo := newMockRepo()
	for _, name := range []string{"Alice", "Bob"} {
		c, err := customer.New(name, name+"@example.com", time.Now().AddDate(-1, 0, 0))
		require.NoError(t, err)
		require.NoError(t, repo.Save(context.Background(), c))
	}

	client := newTestGRPCClient(t, newGRPCHandler(repo))

	resp, err := client.ListCustomers(context.Background(), &customerv1.ListCustomersRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Customers, 2)
}

func TestGRPCHandler_UpdateCustomer(t *testing.T) {
	t.Parallel()

	repo := newMockRepo()
	c, err := customer.New("Alice", "alice@example.com", time.Now().AddDate(-1, 0, 0))
	require.NoError(t, err)
	require.NoError(t, repo.Save(context.Background(), c))

	client := newTestGRPCClient(t, newGRPCHandler(repo))

	t.Run("success", func(t *testing.T) {
		_, err := client.UpdateCustomer(context.Background(), &customerv1.UpdateCustomerRequest{
			Id: c.ID.String(), Name: "Alice Updated", Email: "new@example.com", BirthDate: "1990-01-01",
		})
		require.NoError(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := client.UpdateCustomer(context.Background(), &customerv1.UpdateCustomerRequest{
			Id: uuid.New().String(), Name: "X", Email: "x@x.com", BirthDate: "1990-01-01",
		})
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err))
	})
}

func TestGRPCHandler_RemoveCustomer(t *testing.T) {
	t.Parallel()

	repo := newMockRepo()
	c, err := customer.New("Alice", "alice@example.com", time.Now().AddDate(-1, 0, 0))
	require.NoError(t, err)
	require.NoError(t, repo.Save(context.Background(), c))

	client := newTestGRPCClient(t, newGRPCHandler(repo))

	t.Run("success", func(t *testing.T) {
		_, err := client.RemoveCustomer(context.Background(), &customerv1.RemoveCustomerRequest{Id: c.ID.String()})
		require.NoError(t, err)
	})

	t.Run("not found after removal", func(t *testing.T) {
		_, err := client.RemoveCustomer(context.Background(), &customerv1.RemoveCustomerRequest{Id: c.ID.String()})
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err))
	})
}
