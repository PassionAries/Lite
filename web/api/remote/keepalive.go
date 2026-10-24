package remote

import (
	"time"

	"github.com/gorilla/websocket"
)

func (session *remoteSession) lastActivityAt() time.Time {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.LastActivity
}

func remoteHasRecentTraffic(lastActivity, now time.Time) bool {
	return !lastActivity.IsZero() && now.Sub(lastActivity) < remotePingInterval
}

func writeRemotePing(connection *websocket.Conn, now time.Time) error {
	return connection.WriteControl(websocket.PingMessage, nil, now.Add(remotePingWriteTimeout))
}

// pingWriteErrorEndsSession reports whether a failed protocol ping should tear
// the session down. Data frames can hold gorilla's write lock longer than the
// ping deadline; closing on that timeout is what froze terminals during long
// commands. A dead peer still ends the session through the forward read/write
// errors or the idle read deadline.
func pingWriteErrorEndsSession(error) bool {
	return false
}
