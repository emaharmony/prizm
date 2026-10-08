package v2

import "testing"

func TestDelegationAcknowledgementAndLateCompletion(t *testing.T) {
	dm := NewDelegationManager("tasks", "complete")
	st := NewWorkflowState(&WorkflowConfig{Name: "test"})
	st.RunID = "run-1"
	st.CorrelationID = "corr-1"
	st.Plan = &PlanGraph{Tasks: []PlanTask{{ID: "T1", Agent: "coder", Description: "work"}}}
	del, packet, err := dm.DelegateTask(t.Context(), st.Plan.Tasks[0], st)
	if err != nil {
		t.Fatal(err)
	}
	if packet.DeliveryKey == "" || packet.DelegationID != del.DelegationID {
		t.Fatalf("packet=%+v", packet)
	}
	if !dm.AcknowledgeTask("T1", del.DelegationID, packet.DeliveryKey, st) {
		t.Fatal("ack rejected")
	}
	if !dm.ProgressTask("T1", del.DelegationID, packet.DeliveryKey, st) {
		t.Fatal("progress rejected")
	}
	oldKey := packet.DeliveryKey
	retry, ok := dm.RetryDelegation("T1", st)
	if !ok || retry.DeliveryKey == oldKey {
		t.Fatalf("retry=%+v", retry)
	}
	dm.HandleTaskCompletion(TaskCompletion{TaskID: "T1", DelegationID: del.DelegationID, DeliveryKey: oldKey, Status: "completed", OutputSummary: "late"}, st)
	if st.Delegations[0].Status != "sent" {
		t.Fatalf("late completion changed status to %s", st.Delegations[0].Status)
	}
	dm.HandleTaskCompletion(TaskCompletion{TaskID: "T1", DelegationID: del.DelegationID, DeliveryKey: retry.DeliveryKey, Status: "completed", OutputSummary: "ok"}, st)
	if st.Delegations[0].Status != "completed" {
		t.Fatalf("status=%s", st.Delegations[0].Status)
	}
}
