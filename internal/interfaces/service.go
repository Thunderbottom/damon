package interfaces

import (
	"github.com/hashicorp/nomad/api"
)

// ServicesAPI defines the Nomad Services API interface
type ServicesAPI interface {
	List(q *api.QueryOptions) ([]*api.ServiceRegistrationListStub, *api.QueryMeta, error)
	Get(serviceID string, q *api.QueryOptions) ([]*api.ServiceRegistration, *api.QueryMeta, error)
}
