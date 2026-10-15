package hookcmd

import (
	"math"
	"testing"
	"time"
)

func TestDefinitionTimeout(t *testing.T) {
	valid := []int{0, 1, 3600}
	invalid := []int{-1}
	maxSeconds := math.MaxInt64 / int64(time.Second)
	if int64(int(maxSeconds)) == maxSeconds {
		valid = append(valid, int(maxSeconds))
		invalid = append(invalid, int(maxSeconds)+1)
	}
	for _, seconds := range valid {
		definition := Definition{Name: "guard", Event: UserPromptSubmit, Command: "guard", TimeoutSeconds: seconds}
		if err := definition.Validate(); err != nil {
			t.Errorf("Validate(timeout=%d): %v", seconds, err)
		}
	}
	for _, seconds := range invalid {
		definition := Definition{Name: "guard", Event: UserPromptSubmit, Command: "guard", TimeoutSeconds: seconds}
		if err := definition.Validate(); err == nil {
			t.Errorf("Validate(timeout=%d) accepted an unrepresentable deadline", seconds)
		}
	}
}
