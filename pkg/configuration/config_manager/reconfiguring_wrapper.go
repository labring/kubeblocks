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

package configmanager

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	cfgcore "github.com/apecloud/kubeblocks/pkg/configuration/core"
	viper "github.com/apecloud/kubeblocks/pkg/viperx"
)

// DynamicParamUpdater is designed to adapt to the dapper implementation.
type DynamicParamUpdater interface {
	ExecCommand(ctx context.Context, command string, args ...string) (string, error)
	Close()
}

type mysqlCommandChannel struct {
	db *sql.DB
}

const (
	dbType = "DB_TYPE"

	connectTimeout = 30 * time.Second

	mysql                     = "mysql"
	patroni                   = "patroni"
	mysqlDsnEnv               = "DATA_SOURCE_NAME"
	patroniRestAPIURL         = "PATRONI_REST_API_URL"
	patroniRestAPIUsernameEnv = "PATRONI_RESTAPI_USERNAME"
	patroniRestAPIPasswordEnv = "PATRONI_RESTAPI_PASSWORD"
)

func NewCommandChannel(ctx context.Context, dataType, dsn string) (DynamicParamUpdater, error) {
	// TODO using dapper command channel

	if dataType == "" {
		dataType = viper.GetString(dbType)
	}

	logger.V(1).Info(fmt.Sprintf("new command channel. [%s]", dataType))
	switch strings.ToLower(dataType) {
	case mysql:
		return newMysqlConnection(ctx, dsn)
	case patroni:
		return newPGPatroniConnection(dsn)
	default:
		// TODO mock db begin support dapper
	}
	return nil, cfgcore.MakeError("not supported type[%s]", dataType)
}

func newMysqlDB(ctx context.Context, dsn string) (*sql.DB, error) {
	logger.V(1).Info("connecting mysql.")
	if dsn == "" {
		dsn = os.Getenv(mysqlDsnEnv)
	}
	if dsn == "" {
		return nil, cfgcore.MakeError("require DATA_SOURCE_NAME env.")
	}
	return sql.Open(mysql, dsn)
}

func newMysqlConnection(ctx context.Context, dsn string) (DynamicParamUpdater, error) {
	db, err := newMysqlDB(ctx, dsn)
	if err != nil {
		return nil, cfgcore.WrapError(err, "failed to opening connection to mysql.")
	}
	return newDynamicParamUpdater(ctx, db)
}

func newDynamicParamUpdater(ctx context.Context, db *sql.DB) (DynamicParamUpdater, error) {
	logger.V(1).Info("connecting mysql.")
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// Set max lifetime for a connection.
	db.SetConnMaxLifetime(1 * time.Minute)

	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		logger.Error(err, "failed to ping mysqld.")
		return nil, err
	}
	logger.V(1).Info("succeed to connect to mysql.")
	return &mysqlCommandChannel{db: db}, nil
}

func (m *mysqlCommandChannel) ExecCommand(ctx context.Context, sql string, _ ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	r, err := m.db.ExecContext(ctx, sql)
	if err != nil {
		return "", err
	}
	affected, err := r.RowsAffected()
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(affected, 10), nil
}

func (m *mysqlCommandChannel) Close() {
	m.db.Close()
	logger.V(1).Info("closed mysql connection.")
}

type patroniRESTCredentials struct {
	username string
	password string
}

type pgPatroniCommandChannel struct {
	restapiURL  string
	credentials patroniRESTCredentials
}

func (p *pgPatroniCommandChannel) ExecCommand(ctx context.Context, command string, args ...string) (string, error) {
	const (
		Config  = "config"
		Reload  = "reload"
		Restart = "restart"
	)

	if len(args) == 0 {
		return "", cfgcore.MakeError("patroni is required.")
	}

	functional := args[0]
	restPath := strings.Join([]string{p.restapiURL, functional}, "/")
	logger.V(1).Info(fmt.Sprintf("exec patroni command: %s", functional))
	switch functional {
	case Config:
		return p.sendRestRequest(ctx, command, restPath, http.MethodPatch)
	case Restart, Reload:
		return p.sendRestRequest(ctx, command, restPath, http.MethodPost)
	}
	return "", cfgcore.MakeError("not supported patroni function: [%s]", args[0])
}

func (p *pgPatroniCommandChannel) Close() {
}

func newPGPatroniConnection(hostURL string) (DynamicParamUpdater, error) {
	logger.V(1).Info("connecting patroni.")
	if hostURL == "" {
		hostURL = os.Getenv(patroniRestAPIURL)
	}
	if hostURL == "" {
		return nil, cfgcore.MakeError("require PATRONI_REST_API_URL env.")
	}
	credentials, err := patroniRESTCredentialsFromEnv()
	if err != nil {
		return nil, err
	}

	return &pgPatroniCommandChannel{
		restapiURL:  formatRestAPIPath(hostURL),
		credentials: credentials,
	}, nil
}

func patroniRESTCredentialsFromEnv() (patroniRESTCredentials, error) {
	username, usernameSet := os.LookupEnv(patroniRestAPIUsernameEnv)
	password, passwordSet := os.LookupEnv(patroniRestAPIPasswordEnv)
	if !usernameSet && !passwordSet {
		return patroniRESTCredentials{}, nil
	}
	if !usernameSet || !passwordSet || username == "" || password == "" {
		return patroniRESTCredentials{}, cfgcore.MakeError(
			"%s and %s must be set together and must not be empty",
			patroniRestAPIUsernameEnv, patroniRestAPIPasswordEnv,
		)
	}
	return patroniRESTCredentials{username: username, password: password}, nil
}

func (p *pgPatroniCommandChannel) sendRestRequest(ctx context.Context, body string, url string, method string) (string, error) {
	client := &http.Client{}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	// create new HTTP PATCH request with JSON payload
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return "", err
	}

	// set content-type header to JSON
	req.Header.Set("Content-Type", "application/json")
	if p.credentials.username != "" || p.credentials.password != "" {
		if p.credentials.username == "" || p.credentials.password == "" {
			return "", cfgcore.MakeError(
				"%s and %s must be set together",
				patroniRestAPIUsernameEnv, patroniRestAPIPasswordEnv,
			)
		}
		req.SetBasicAuth(p.credentials.username, p.credentials.password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	response, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", cfgcore.MakeError(
			"patroni %s request returned %s",
			method, resp.Status,
		)
	}
	return string(response), nil
}

func formatRestAPIPath(path string) string {
	const restAPIPrefix = "http://"
	if strings.HasPrefix(path, restAPIPrefix) {
		return path
	}
	return fmt.Sprintf("%s%s", restAPIPrefix, path)
}
