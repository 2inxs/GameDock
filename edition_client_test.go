//go:build client

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConnectEditionHasNoServerBindingsOrDatabase(t *testing.T) {
	t.Setenv("GAMEDOCK_DATA_DIR", t.TempDir())
	a:=NewApp();a.startup(context.Background());defer a.shutdown(context.Background())
	if a.startupError!=nil{t.Fatal(a.startupError)}
	info:=a.GetAppInfo()
	if info.Edition!="client"||info.Capabilities.Servers||info.Capabilities.Hosting||!info.Capabilities.Rooms{t.Fatalf("invalid client capabilities: %+v",info)}
	bindings:=reflect.TypeOf(a)
	for _,name:=range []string{"ListServers","StartServer","StopServer","KillServer","SaveServer","DeleteServer","SendCommand","CreateRoom","GetSettings","SaveSettings","PickJava","PickJar"}{
		if _,present:=bindings.MethodByName(name);present{t.Errorf("server binding %s leaked into Connect edition",name)}
	}
	for _,name:=range []string{"ListRooms","JoinRoom","ConnectRoom","DisconnectRoom","InstallNetworkEngine","SaveRoomSettings"}{
		if _,present:=bindings.MethodByName(name);!present{t.Errorf("connection binding %s missing",name)}
	}
	if _,err:=os.Stat(filepath.Join(info.DataDir,"gamedock.db"));!os.IsNotExist(err){t.Fatalf("Connect edition initialized server database: %v",err)}
}
