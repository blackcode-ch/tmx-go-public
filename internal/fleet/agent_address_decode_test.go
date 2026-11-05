package fleet

import "testing"

func TestDecodeRoomAgentAddress(t *testing.T) {
	for _, input := range []string{"worker@HOST", "WORKER@HOST", "Worker@Host", "tmx:HOST:worker", "TMX:HOST:WORKER", " tMx:Host:Worker "} {
		name, host, qualified, err := DecodeAgentAddress(input)
		if err != nil || name != "worker" || host != "host" || !qualified {
			t.Fatalf("%q: %s %s %v %v", input, name, host, qualified, err)
		}
	}
	for _, input := range []string{"tmx::worker", "tmx:host:", "tmx:host:worker:extra", "tmx:host:w@other", "worker@host@other", "worker@", "@host", "TMX:host:BAD NAME", "tmx:bad host:worker", "tmx:https://host:worker"} {
		if _, _, _, err := DecodeAgentAddress(input); KindOf(err) != KindInvalid {
			t.Fatalf("accepted %q: %v", input, err)
		}
	}
}
