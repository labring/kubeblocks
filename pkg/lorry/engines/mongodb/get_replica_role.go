/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package mongodb

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/apecloud/kubeblocks/pkg/lorry/dcs"
)

func (mgr *Manager) GetReplicaRole(ctx context.Context, cluster *dcs.Cluster) (string, error) {
	role, err := mgr.GetMemberState(ctx)
	if err != nil {
		mgr.Logger.Info("get replica state failed", "error", err.Error())
		// Return "" to indicate unknown role (not an error - prevents readiness probe failure)
		// Auto-recovery is handled in IsDBStartupReady with a proper fresh context.
		return "", nil
	}

	return role, nil
}

// tryAutoRecoverFromRestore detects and fixes the "restored from backup" scenario where:
// 1. RS is not initialized (no replset config in restored data)
// 2. Credentials are mismatched (old users in data, new password in secret)
// This is needed because MongoDB's HA module is disabled (IsHAAvailable returns false),
// so the normal HA startup flow (IsClusterInitialized -> InitializeCluster -> CreateRoot)
// never runs.
func (mgr *Manager) tryAutoRecoverFromRestore(ctx context.Context, cluster *dcs.Cluster) error {
	// Step 1: Use isMaster to check if RS is initialized (works without auth on direct connection)
	resp, isMasterErr := mgr.getLocalIsMaster(ctx)
	if isMasterErr == nil && resp != nil {
		// isMaster succeeded. Check if RS is configured.
		if resp.SetName != "" {
			// RS is initialized - the issue is likely just auth mismatch.
			mgr.Logger.Info("RS is initialized (isMaster reports setName), syncing root password", "setName", resp.SetName)
			return mgr.syncRootPassword(ctx)
		}
		// isMaster returned OK but no setName - RS is not initialized
		mgr.Logger.Info("RS not initialized (isMaster has no setName), initiating replica set")
		return mgr.initializeFromRestore(ctx, cluster)
	}

	// isMaster failed - try replSetGetStatus via unauth client for more info
	mgr.Logger.Info("isMaster failed, trying replSetGetStatus via unauth client", "error", isMasterErr)
	unauthClient, err := NewLocalUnauthClient(ctx)
	if err != nil {
		mgr.Logger.Info("Cannot create unauth client for recovery check", "error", err)
		return err
	}
	defer unauthClient.Disconnect(ctx) //nolint:errcheck

	rsStatus, err := GetReplSetStatus(ctx, unauthClient)
	if rsStatus != nil && rsStatus.Set != "" {
		mgr.Logger.Info("RS is initialized but auth client can't connect, syncing root password")
		return mgr.syncRootPassword(ctx)
	}

	if err != nil {
		causedErr := errors.Cause(err)
		cmdErr, ok := causedErr.(mongo.CommandError)
		if !ok {
			return err
		}

		switch cmdErr.Name {
		case "NotYetInitialized":
			mgr.Logger.Info("RS not yet initialized, initiating replica set")
			return mgr.initializeFromRestore(ctx, cluster)

		case "Unauthorized":
			mgr.Logger.Info("Unauth client got Unauthorized (users exist from backup)")
			return mgr.initializeFromRestoreWithAuth(ctx, cluster)

		default:
			mgr.Logger.Info("Unexpected error checking RS status", "error", cmdErr.Name)
			return err
		}
	}

	return nil
}

// initializeFromRestore handles the case where RS is not initialized.
func (mgr *Manager) initializeFromRestore(ctx context.Context, cluster *dcs.Cluster) error {
	// Initialize the replica set
	if err := mgr.InitializeCluster(ctx, cluster); err != nil {
		mgr.Logger.Info("RS initiate failed", "error", err)
		return err
	}
	mgr.Logger.Info("RS initiated successfully from restore recovery")

	// Wait a moment for RS to elect primary
	time.Sleep(2 * time.Second)

	// Create root user with current credentials
	if err := mgr.CreateRoot(ctx); err != nil {
		mgr.Logger.Info("Create root failed after RS init", "error", err)
		return err
	}
	mgr.Logger.Info("Root user created successfully from restore recovery")

	return nil
}

// initializeFromRestoreWithAuth handles the case where users exist from backup
// but RS may or may not be initialized.
func (mgr *Manager) initializeFromRestoreWithAuth(ctx context.Context, cluster *dcs.Cluster) error {
	// Use isMaster via unauth to check if RS is configured (doesn't require auth)
	resp, err := mgr.getLocalIsMaster(ctx)
	if err == nil && resp != nil && resp.SetName == "" {
		// RS not initialized - try to initiate
		mgr.Logger.Info("RS not initialized (confirmed via isMaster), initiating")
		if initErr := mgr.InitializeCluster(ctx, cluster); initErr != nil {
			mgr.Logger.Info("RS initiate via auth path failed", "error", initErr)
			return initErr
		}
		mgr.Logger.Info("RS initiated successfully via auth path")
		time.Sleep(2 * time.Second)
	} else if err == nil && resp != nil && resp.SetName != "" {
		mgr.Logger.Info("RS is already initialized")
	}

	// Sync root password to match current secret
	return mgr.syncRootPassword(ctx)
}

// syncRootPassword attempts to update the root user's password to match
// the current secret credentials.
func (mgr *Manager) syncRootPassword(ctx context.Context) error {
	// Try creating root (handles both create and update-on-failure via unauth/auth fallback)
	if err := mgr.CreateRoot(ctx); err != nil {
		mgr.Logger.Info("CreateRoot during password sync failed", "error", err)
		return err
	}
	mgr.Logger.Info("Root password synced successfully")

	// Reconnect with new credentials
	return mgr.reconnectClient(ctx)
}
