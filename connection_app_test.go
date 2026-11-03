package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"gamedock/internal/rooms"
)

func TestRoomUIBoundaryDoesNotExposeCredentials(t *testing.T){
	t.Setenv("GAMEDOCK_DATA_DIR",t.TempDir())
	a:=NewApp();a.startup(context.Background());defer a.shutdown(context.Background())
	if a.startupError!=nil{t.Fatal(a.startupError)}
	secret:="private-room-key-0123456789"
	c,err:=a.roomStore.Save(rooms.Config{Name:"Room",Secret:secret,Role:"host",BootstrapPeers:[]string{"tcp://127.0.0.1:12000"}})
	if err!=nil{t.Fatal(err)}
	invite,err:=rooms.Invitation(c);if err!=nil{t.Fatal(err)}
	joined,err:=a.JoinRoom(invite,25566,false);if err!=nil{t.Fatal(err)}
	encoded,err:=json.Marshal(joined);if err!=nil{t.Fatal(err)}
	if strings.Contains(string(encoded),secret)||strings.Contains(string(encoded),c.NetworkName){t.Fatal("room credentials leaked through UI summary")}
	if joined.Config.Role!="guest"||joined.Config.AllowRelay{t.Fatal("invitation changed local role or enabled relay")}
	if err:=a.SaveRoomSettings(joined.Config.ID,25567,true,[]string{"udp://127.0.0.1:12001"});err!=nil{t.Fatal(err)}
	saved,err:=a.findRoom(joined.Config.ID);if err!=nil{t.Fatal(err)}
	if saved.LocalPort!=25567||!saved.AllowRelay||saved.Secret!=secret{t.Fatal("updating local connection settings lost membership")}
	if _,err=a.JoinRoom(invite,25568,false);err==nil{t.Fatal("duplicate invitation created a second guest entry")}
	if err=a.DeleteRoom(joined.Config.ID);err!=nil{t.Fatal(err)}
}
