// This file is part of arduino-cli.
//
// Copyright 2022 ARDUINO SA (http://www.arduino.cc/)
//
// This software is released under the GNU General Public License version 3,
// which covers the main part of arduino-cli.
// The terms of this license can be found at:
// https://www.gnu.org/licenses/gpl-3.0.en.html
//
// You can be released from the requirements of the above licenses by purchasing
// a commercial license. Buying such a license is mandatory if you want to
// modify or otherwise use the software for commercial activities involving the
// Arduino software without disclosing the source code of your own applications.
// To purchase a commercial license, send an email to license@arduino.cc.

package daemon_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/arduino/arduino-cli/rpc/cc/arduino/cli/commands/v1"
	"github.com/stretchr/testify/require"
)

func TestArduinoCliDaemon(t *testing.T) {
	// See: https://github.com/arduino/arduino-cli/pull/1804

	t.SkipNow() // TO BE Removed once the bug is fixed

	env, cli := createEnvForDaemon(t)
	defer env.CleanUp()

	grpcInst := cli.Create()
	require.NoError(t, grpcInst.Init("", "", func(ir *commands.InitResponse) {
		fmt.Printf("INIT> %v\n", ir.GetMessage())
	}))

	// Run a one-shot board list
	boardListResp, err := grpcInst.BoardList(time.Second)
	require.NoError(t, err)
	fmt.Printf("Got boardlist response with %d ports\n", len(boardListResp.GetPorts()))

	// Run a one-shot board list again (should not fail)
	boardListResp, err = grpcInst.BoardList(time.Second)
	require.NoError(t, err)
	fmt.Printf("Got boardlist response with %d ports\n", len(boardListResp.GetPorts()))

	testWatcher := func() {
		// Run watcher
		watcher, err := grpcInst.BoardListWatch()
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			defer cancel()
			for {
				msg, err := watcher.Recv()
				if err == io.EOF {
					fmt.Println("Watcher EOF")
					return
				}
				require.Empty(t, msg.Error, "Board list watcher returned an error")
				require.NoError(t, err, "BoardListWatch grpc call returned an error")
				fmt.Printf("WATCH> %v\n", msg)
			}
		}()
		time.Sleep(time.Second)
		require.NoError(t, watcher.CloseSend())
		select {
		case <-ctx.Done():
			// all right!
		case <-time.After(time.Second):
			require.Fail(t, "BoardListWatch didn't close")
		}
	}

	testWatcher()
	testWatcher()
}
