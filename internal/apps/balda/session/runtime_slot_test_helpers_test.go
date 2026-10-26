package session

import adksession "google.golang.org/adk/v2/session"

func withTestRuntime(ts *TopicSession, service adksession.Service, sess adksession.Session, closeFn func() error) *TopicSession {
	if ts == nil {
		ts = &TopicSession{}
	}
	ts.runtime = newRuntimeSlot(runtimeSlotConfig{
		runtime:   &BuiltRuntime{SessionSvc: service, Close: closeFn},
		session:   sess,
		userID:    ts.userID,
		sessionID: ts.sessionID,
	})
	return ts
}

func testRuntimeService(ts *TopicSession) adksession.Service {
	value, _ := ts.runtimeValue()
	return value.runtime.SessionSvc
}

func testRuntimeSession(ts *TopicSession) adksession.Session {
	value, _ := ts.runtimeValue()
	return value.session
}
