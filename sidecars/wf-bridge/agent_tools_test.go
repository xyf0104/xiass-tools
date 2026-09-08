package main

import (
	"errors"
	"testing"

	"antigravity-wf-assistant/internal/agent"
)

func TestLaunchDetectedAgentUsesOnlyVerifiedCursorAndWindsurfTargets(t *testing.T) {
	t.Run("macos uses the verified app bundle rather than the executable path", func(t *testing.T) {
		var launched string
		result := launchDetectedAgent(agent.CursorID, launchAggregate(agent.CursorID, agent.StateReady, "/Applications/Cursor.app", "/Applications/Cursor.app/Contents/MacOS/Cursor"), "darwin", func(path string) error {
			launched = path
			return nil
		})
		if !result.OK || launched != "/Applications/Cursor.app" {
			t.Fatalf("result = %+v, launched = %q", result, launched)
		}
	})

	t.Run("windows uses the verified executable", func(t *testing.T) {
		var launched string
		result := launchDetectedAgent(agent.WindsurfID, launchAggregate(agent.WindsurfID, agent.StateReady, `C:\Users\user\AppData\Local\Programs\Windsurf`, `C:\Users\user\AppData\Local\Programs\Windsurf\Windsurf.exe`), "windows", func(path string) error {
			launched = path
			return nil
		})
		if !result.OK || launched != `C:\Users\user\AppData\Local\Programs\Windsurf\Windsurf.exe` {
			t.Fatalf("result = %+v, launched = %q", result, launched)
		}
	})
}

func TestLaunchDetectedAgentRejectsUnverifiedAndNonDesktopTargets(t *testing.T) {
	t.Run("does not launch an incomplete installation", func(t *testing.T) {
		called := false
		result := launchDetectedAgent(agent.CursorID, launchAggregate(agent.CursorID, agent.StateDegraded, "/Applications/Cursor.app", ""), "darwin", func(string) error {
			called = true
			return nil
		})
		if result.OK || called || result.Message == "" {
			t.Fatalf("result = %+v, called = %t", result, called)
		}
	})

	t.Run("does not launch an executable outside the discovered application", func(t *testing.T) {
		called := false
		result := launchDetectedAgent(agent.CursorID, launchAggregate(agent.CursorID, agent.StateReady, "/Applications/Cursor.app", "/Applications/Other.app/Contents/MacOS/Other"), "darwin", func(string) error {
			called = true
			return nil
		})
		if result.OK || called || result.Message == "" {
			t.Fatalf("result = %+v, called = %t", result, called)
		}
	})

	t.Run("allows a verified application when only its MCP configuration is degraded", func(t *testing.T) {
		called := false
		result := launchDetectedAgent(agent.WindsurfID, launchAggregate(agent.WindsurfID, agent.StateDegraded, "/Applications/Windsurf.app", "/Applications/Windsurf.app/Contents/MacOS/Windsurf"), "darwin", func(string) error {
			called = true
			return nil
		})
		if !result.OK || !called {
			t.Fatalf("result = %+v, called = %t", result, called)
		}
	})

	t.Run("does not use a generic launcher for codex or claude", func(t *testing.T) {
		for _, identifier := range []agent.ID{agent.CodexID, agent.ClaudeCodeID, agent.AntigravityID} {
			called := false
			result := launchDetectedAgent(identifier, launchAggregate(identifier, agent.StateReady, "/Applications/Other.app", "/Applications/Other.app/Contents/MacOS/Other"), "darwin", func(string) error {
				called = true
				return nil
			})
			if result.OK || called || result.Message == "" {
				t.Fatalf("%s result = %+v, called = %t", identifier, result, called)
			}
		}
	})

	t.Run("does not expose platform launch errors", func(t *testing.T) {
		result := launchDetectedAgent(agent.CursorID, launchAggregate(agent.CursorID, agent.StateReady, "/Applications/Cursor.app", "/Applications/Cursor.app/Contents/MacOS/Cursor"), "darwin", func(string) error {
			return errors.New("/private/path/must-not-reach-renderer")
		})
		if result.OK || result.Message == "" || result.Message == "/private/path/must-not-reach-renderer" {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestLaunchDetectedAgentRequiresPlatformSpecificVerifiedTarget(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		platform   string
		root       string
		executable string
	}{
		{name: "macos data directory is not executable", platform: "darwin", root: "/Users/test/Library/Application Support/Cursor", executable: ""},
		{name: "windows missing executable", platform: "windows", root: `C:\Users\test\AppData\Local\Programs\Cursor`, executable: ""},
		{name: "windows executable outside root", platform: "windows", root: `C:\Users\test\AppData\Local\Programs\Cursor`, executable: `C:\Windows\System32\not-cursor.exe`},
		{name: "unsupported host", platform: "linux", root: "/Applications/Cursor.app", executable: "/Applications/Cursor.app/Contents/MacOS/Cursor"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			called := false
			result := launchDetectedAgent(agent.CursorID, launchAggregate(agent.CursorID, agent.StateDetected, testCase.root, testCase.executable), testCase.platform, func(string) error {
				called = true
				return nil
			})
			if result.OK || called {
				t.Fatalf("result = %+v, called = %t", result, called)
			}
		})
	}
}

func launchAggregate(identifier agent.ID, state agent.State, root, executable string) agent.AggregateStatus {
	return agent.AggregateStatus{Agents: []agent.Status{{
		AgentID: identifier,
		State:   state,
		Installation: agent.Installation{
			Root:           root,
			ExecutablePath: executable,
		},
	}}}
}
