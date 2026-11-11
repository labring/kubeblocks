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
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/DATA-DOG/go-sqlmock"

	cfgcore "github.com/apecloud/kubeblocks/pkg/configuration/core"
)

var _ = Describe("Handler Util Test", func() {

	BeforeEach(func() {
		// Add any setup steps that needs to be executed before each test
	})

	AfterEach(func() {
	})

	Context("TestReconfiguringWrapper", func() {
		It("TestNewCommandChannel", func() {
			By("testMysql")
			_, err := NewCommandChannel(ctx, mysql, "")
			Expect(err).ShouldNot(Succeed())
			_, err = NewCommandChannel(ctx, mysql, "dsn")
			Expect(err).ShouldNot(Succeed())

			By("testPatroni")
			_, err = NewCommandChannel(ctx, patroni, "")
			Expect(err).ShouldNot(Succeed())
			Expect(err.Error()).Should(ContainSubstring("PATRONI_REST_API_URL"))

			_, err = NewCommandChannel(ctx, patroni, "localhost")
			Expect(err).Should(Succeed())

			By("testUnsupported")
			_, err = NewCommandChannel(ctx, "", "localhost")
			Expect(err).ShouldNot(Succeed())
			Expect(err.Error()).Should(ContainSubstring("not supported type"))
		})
	})

	Context("TestPGPatroniConnection", func() {
		setCredentials := func(username, password string) {
			os.Setenv(patroniRestAPIUsernameEnv, username)
			os.Setenv(patroniRestAPIPasswordEnv, password)
			DeferCleanup(os.Unsetenv, patroniRestAPIUsernameEnv)
			DeferCleanup(os.Unsetenv, patroniRestAPIPasswordEnv)
		}
		unsetCredentials := func() {
			os.Unsetenv(patroniRestAPIUsernameEnv)
			os.Unsetenv(patroniRestAPIPasswordEnv)
			DeferCleanup(os.Unsetenv, patroniRestAPIUsernameEnv)
			DeferCleanup(os.Unsetenv, patroniRestAPIPasswordEnv)
		}

		It("validates REST API credentials", func() {
			By("allowing authentication to be disabled")
			unsetCredentials()
			conn, err := newPGPatroniConnection("localhost")
			Expect(err).Should(Succeed())
			channel, ok := conn.(*pgPatroniCommandChannel)
			Expect(ok).Should(BeTrue())
			Expect(channel.credentials).Should(Equal(patroniRESTCredentials{}))

			By("allowing a username and password pair")
			setCredentials("patroni-user", "patroni-password")
			conn, err = newPGPatroniConnection("localhost")
			Expect(err).Should(Succeed())
			channel, ok = conn.(*pgPatroniCommandChannel)
			Expect(ok).Should(BeTrue())
			Expect(channel.credentials).Should(Equal(patroniRESTCredentials{
				username: "patroni-user",
				password: "patroni-password",
			}))

			By("rejecting partial credentials")
			setCredentials("patroni-user", "")
			_, err = newPGPatroniConnection("localhost")
			Expect(err).ShouldNot(Succeed())
			Expect(err.Error()).Should(ContainSubstring("must be set together"))

			By("rejecting empty credentials when authentication is configured")
			setCredentials("", "")
			_, err = newPGPatroniConnection("localhost")
			Expect(err).ShouldNot(Succeed())
			Expect(err.Error()).Should(ContainSubstring("must not be empty"))
		})

		It("sends Basic Auth to every Patroni endpoint", func() {
			requests := make([]string, 0)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				username, password, ok := r.BasicAuth()
				if !ok || username != "patroni-user" || password != "patroni-password" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				requests = append(requests, r.Method+" "+r.URL.Path)
				w.WriteHeader(http.StatusOK)
			}))
			DeferCleanup(server.Close)

			channel := &pgPatroniCommandChannel{
				restapiURL: server.URL,
				credentials: patroniRESTCredentials{
					username: "patroni-user",
					password: "patroni-password",
				},
			}
			_, err := channel.ExecCommand(ctx, `{"pg_hba":[]}`, "config")
			Expect(err).Should(Succeed())
			_, err = channel.ExecCommand(ctx, "", "reload")
			Expect(err).Should(Succeed())
			_, err = channel.ExecCommand(ctx, "", "restart")
			Expect(err).Should(Succeed())
			Expect(requests).Should(Equal([]string{
				"PATCH /config",
				"POST /reload",
				"POST /restart",
			}))
		})

		It("does not send Authorization when authentication is disabled", func() {
			authorized := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorized = r.Header.Get("Authorization") != ""
				w.WriteHeader(http.StatusOK)
			}))
			DeferCleanup(server.Close)

			channel := &pgPatroniCommandChannel{restapiURL: server.URL}
			_, err := channel.ExecCommand(ctx, "", "reload")
			Expect(err).Should(Succeed())
			Expect(authorized).Should(BeFalse())
		})

		It("rejects non-2xx responses", func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("unauthorized"))
			}))
			DeferCleanup(server.Close)

			channel := &pgPatroniCommandChannel{
				restapiURL: server.URL,
				credentials: patroniRESTCredentials{
					username: "patroni-user",
					password: "patroni-password",
				},
			}
			result, err := channel.ExecCommand(ctx, `{"pg_hba":[]}`, "config")
			Expect(err).ShouldNot(Succeed())
			Expect(err.Error()).Should(ContainSubstring("401 Unauthorized"))
			Expect(result).Should(BeEmpty())
		})
	})

	Context("TestMysqlDB", func() {

		It("TestMysqlDB connect failed", func() {
			db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
			Expect(err).Should(Succeed())
			defer db.Close()

			mock.ExpectPing().WillReturnError(cfgcore.MakeError("ping error"))

			_, err = newDynamicParamUpdater(ctx, db)
			Expect(err).ShouldNot(Succeed())
			Expect(err.Error()).Should(ContainSubstring("ping error"))
		})

		It("TestMysqlDB update parameter", func() {
			testSQLString := "SET GLOBAL max_connections = 1000"

			db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
			Expect(err).Should(Succeed())

			mock.ExpectPing().WillDelayFor(10 * time.Millisecond)
			mock.ExpectExec(testSQLString).WillReturnError(cfgcore.MakeError("failed to set parameter"))
			mock.ExpectExec(testSQLString).WillReturnResult(sqlmock.NewResult(0, 0))

			By("create DynamicParamUpdater")
			dUpdater, err := newDynamicParamUpdater(ctx, db)
			Expect(err).Should(Succeed())
			defer dUpdater.Close()

			By("test dynamic parameter")
			_, err = dUpdater.ExecCommand(ctx, testSQLString)
			Expect(err).ShouldNot(Succeed())
			r, err := dUpdater.ExecCommand(ctx, testSQLString)
			Expect(err).Should(Succeed())
			Expect("0").Should(BeEquivalentTo(r))
		})

	})
})
