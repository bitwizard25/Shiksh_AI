package usecase_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestBuildHistory(t *testing.T) {
	msgs := []entity.Message{
		{Role: entity.RoleLearner, Content: "q1", Status: entity.MessageComplete},
		{Role: entity.RoleTutor, Content: "a1", Status: entity.MessageComplete},
		{Role: entity.RoleLearner, Content: "q2", Status: entity.MessageComplete},
		{Role: entity.RoleTutor, Content: "heard part", Status: entity.MessageInterrupted},
		{Role: entity.RoleLearner, Content: "q3", Status: entity.MessageComplete},
		{Role: entity.RoleTutor, Content: "never played", Status: entity.MessageFailed},
		{Role: entity.RoleLearner, Content: "", Status: entity.MessageComplete},
	}
	got := usecase.BuildHistory(msgs, 0)
	want := []usecase.ChatMessage{
		{usecase.ChatUser, "q1"}, {usecase.ChatModel, "a1"}, {usecase.ChatUser, "q2"},
		{usecase.ChatModel, "heard part (learner interrupted)"}, {usecase.ChatUser, "q3"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
}

func TestBuildHistoryKeepsTheLastN(t *testing.T) {
	var msgs []entity.Message
	for i := range 30 {
		role := entity.RoleLearner
		if i%2 == 1 {
			role = entity.RoleTutor
		}
		msgs = append(msgs, entity.Message{Role: role, Content: fmt.Sprint("m", i), Status: entity.MessageComplete})
	}
	got := usecase.BuildHistory(msgs, 20)
	if len(got) != 20 || got[0].Text != "m10" || got[19].Text != "m29" {
		t.Fatalf("got %d messages, first %q last %q", len(got), got[0].Text, got[19].Text)
	}
}
