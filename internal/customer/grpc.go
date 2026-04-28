package customer

import (
	"context"
	"errors"
	"time"

	customerv1 "github.com/eduardo-sl/go-blueprint/gen/customer/v1"
	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCHandler implements customerv1.CustomerServiceServer.
// It translates gRPC requests → domain commands → gRPC responses.
// No business logic lives here — all decisions are delegated to Service and QueryService.
type GRPCHandler struct {
	customerv1.UnimplementedCustomerServiceServer
	svc      *Service
	query    querier
	eventLog eventlog.Store
}

func NewGRPCHandler(svc *Service, query querier, el eventlog.Store) *GRPCHandler {
	return &GRPCHandler{svc: svc, query: query, eventLog: el}
}

func (h *GRPCHandler) RegisterCustomer(
	ctx context.Context,
	req *customerv1.RegisterCustomerRequest,
) (*customerv1.RegisterCustomerResponse, error) {
	birthDate, err := time.Parse("2006-01-02", req.BirthDate)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid birth_date format: %v", err)
	}

	id, err := h.svc.Register(ctx, RegisterCmd{
		Name:      req.Name,
		Email:     req.Email,
		BirthDate: birthDate,
	})
	if err != nil {
		return nil, mapDomainErrorToGRPC(err)
	}

	return &customerv1.RegisterCustomerResponse{Id: id.String()}, nil
}

func (h *GRPCHandler) GetCustomer(
	ctx context.Context,
	req *customerv1.GetCustomerRequest,
) (*customerv1.GetCustomerResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid customer id: %v", err)
	}

	c, err := h.query.GetByID(ctx, id)
	if err != nil {
		return nil, mapDomainErrorToGRPC(err)
	}

	return &customerv1.GetCustomerResponse{Customer: toProto(c)}, nil
}

func (h *GRPCHandler) ListCustomers(
	ctx context.Context,
	_ *customerv1.ListCustomersRequest,
) (*customerv1.ListCustomersResponse, error) {
	customers, err := h.query.List(ctx)
	if err != nil {
		return nil, mapDomainErrorToGRPC(err)
	}

	out := make([]*customerv1.Customer, len(customers))
	for i, c := range customers {
		out[i] = toProto(c)
	}
	return &customerv1.ListCustomersResponse{Customers: out}, nil
}

func (h *GRPCHandler) UpdateCustomer(
	ctx context.Context,
	req *customerv1.UpdateCustomerRequest,
) (*customerv1.UpdateCustomerResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid customer id: %v", err)
	}

	birthDate, err := time.Parse("2006-01-02", req.BirthDate)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid birth_date format: %v", err)
	}

	if err := h.svc.Update(ctx, UpdateCmd{
		ID:        id,
		Name:      req.Name,
		Email:     req.Email,
		BirthDate: birthDate,
	}); err != nil {
		return nil, mapDomainErrorToGRPC(err)
	}

	return &customerv1.UpdateCustomerResponse{}, nil
}

func (h *GRPCHandler) RemoveCustomer(
	ctx context.Context,
	req *customerv1.RemoveCustomerRequest,
) (*customerv1.RemoveCustomerResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid customer id: %v", err)
	}

	if err := h.svc.Remove(ctx, id); err != nil {
		return nil, mapDomainErrorToGRPC(err)
	}

	return &customerv1.RemoveCustomerResponse{}, nil
}

// WatchCustomerEvents streams new events from the event log to the client.
// It polls every 2 seconds and sends any events that occurred after the last
// sent event. The stream ends when the client disconnects.
func (h *GRPCHandler) WatchCustomerEvents(
	req *customerv1.WatchCustomerEventsRequest,
	stream customerv1.CustomerService_WatchCustomerEventsServer,
) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var since time.Time

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-ticker.C:
			events, err := h.eventLog.FetchSince(stream.Context(), req.AggregateId, since)
			if err != nil {
				return status.Errorf(codes.Internal, "event log: %v", err)
			}
			for _, e := range events {
				if err := stream.Send(&customerv1.CustomerEvent{
					EventType:   e.EventType,
					AggregateId: e.AggregateID.String(),
					Payload:     e.Payload,
					OccurredAt:  timestamppb.New(e.OccurredAt),
				}); err != nil {
					return err
				}
				since = e.OccurredAt
			}
		}
	}
}

func toProto(c Customer) *customerv1.Customer {
	return &customerv1.Customer{
		Id:        c.ID.String(),
		Name:      c.Name,
		Email:     c.Email,
		BirthDate: c.BirthDate.Format("2006-01-02"),
		CreatedAt: timestamppb.New(c.CreatedAt),
		UpdatedAt: timestamppb.New(c.UpdatedAt),
	}
}

// mapDomainErrorToGRPC translates customer sentinel errors to gRPC status codes.
// Internal errors always return codes.Internal with a generic message to avoid
// leaking implementation details to gRPC callers.
func mapDomainErrorToGRPC(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrEmailExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrInvalidBirthDate):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrNameRequired):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrEmailRequired):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
