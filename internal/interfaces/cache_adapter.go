package interfaces

import (
	"context"
	"github.com/valkey-io/valkey-go"
)

// ValkeyClientAdapter adapts valkey.Client to interfaces.CacheClient
type ValkeyClientAdapter struct {
	client valkey.Client
}

// NewValkeyClientAdapter creates a new adapter for the valkey client
func NewValkeyClientAdapter(client valkey.Client) CacheClient {
	return &ValkeyClientAdapter{client: client}
}

func (a *ValkeyClientAdapter) Close() {
	a.client.Close()
}

func (a *ValkeyClientAdapter) Do(ctx context.Context, cmd any) any {
	if completed, ok := cmd.(valkey.Completed); ok {
		return a.client.Do(ctx, completed)
	}
	return nil
}

func (a *ValkeyClientAdapter) RawClient() valkey.Client {
	return a.client
}
