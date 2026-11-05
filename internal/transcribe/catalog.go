package transcribe

import (
	_ "embed"
	"strings"
)

// What gets downloaded, pinned. The engine is whisper.cpp's whisper-cli, built by tmx's own CI
// (.github/workflows/whisper-engine.yml) from the tag in engine.pin and published as the
// release whisper-engine-<tag>; it is trusted through that release's signed SHA256SUMS (the
// update key), so no hash of it lives here. The models come from Hugging Face, pinned by commit
// and SHA-256 (checked against the Hugging Face API: the LFS object ids).

//go:embed engine.pin
var enginePin string

// EngineTag is the whisper.cpp release the engine is built from, e.g. "v1.9.5".
var EngineTag = pinValue("tag")

// EngineRelease is the tag of the tmx release that carries the engine binaries.
func EngineRelease() string { return "whisper-engine-" + EngineTag }

func pinValue(key string) string {
	for _, line := range strings.Split(enginePin, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	panic("engine.pin has no " + key)
}

// enginePlatforms are the platforms CI builds the engine for.
var enginePlatforms = map[string]bool{
	"darwin/arm64":  true, // Metal
	"darwin/amd64":  true, // CPU (AVX2)
	"linux/amd64":   true, // CPU (AVX2)
	"linux/arm64":   true, // CPU
	"windows/amd64": true, // CPU (AVX2)
}

// engineAsset is the release file of the engine for a platform ("" = none is built).
func engineAsset(goos, goarch string) string {
	if !enginePlatforms[goos+"/"+goarch] {
		return ""
	}
	return "whisper-cli_" + goos + "_" + goarch + exeExt(goos)
}

func exeExt(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

// Model is a file of the catalog.
type Model struct {
	Name  string // catalog name: config voice.model, the API's {name}
	File  string // file name in voice/models
	Label string
	Size  int64
	// SHA256 is the file's hash (Hugging Face's LFS object id); Repo and Revision pin where it
	// comes from: https://huggingface.co/<Repo>/resolve/<Revision>/<File>.
	SHA256, Repo, Revision string
	RAMMB                  int  // roughly what the engine needs with it
	Heavy                  bool // better only on machines with 16 GB of RAM or more
	VAD                    bool // the voice activity model, not a speech model
}

const (
	hfWhisper    = "ggerganov/whisper.cpp"
	hfWhisperRev = "5359861c739e955e79d9a303bcbc70fb988958b1"
	hfVAD        = "ggml-org/whisper-vad"
	hfVADRev     = "9ffd54a1e1ee413ddf265af9913beaf518d1639b"
)

// DefaultModel is the model jobs use unless config voice.model names another.
const DefaultModel = "base-q5_1"

// Models are the speech models, smallest first. All are multilingual.
var Models = []Model{
	{Name: "base-q5_1", File: "ggml-base-q5_1.bin", Label: "Base", Size: 59707625,
		SHA256: "422f1ae452ade6f30a004d7e5c6a43195e4433bc370bf23fac9cc591f01a8898", Repo: hfWhisper, Revision: hfWhisperRev, RAMMB: 400},
	{Name: "small-q5_1", File: "ggml-small-q5_1.bin", Label: "Small", Size: 190085487,
		SHA256: "ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb", Repo: hfWhisper, Revision: hfWhisperRev, RAMMB: 900, Heavy: true},
	{Name: "large-v3-turbo-q5_0", File: "ggml-large-v3-turbo-q5_0.bin", Label: "Large v3 Turbo", Size: 574041195,
		SHA256: "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2", Repo: hfWhisper, Revision: hfWhisperRev, RAMMB: 1800, Heavy: true},
}

// VADModel is Silero's voice activity model (whisper-cli --vad): it skips silence, so the
// speech model invents fewer words there. Downloaded with the setup; small.
var VADModel = Model{Name: "silero-v6.2.0", File: "ggml-silero-v6.2.0.bin", Label: "Silero voice activity", Size: 885098,
	SHA256: "2aa269b785eeb53a82983a20501ddf7c1d9c48e33ab63a41391ac6c9f7fb6987", Repo: hfVAD, Revision: hfVADRev, RAMMB: 20, VAD: true}

// FindModel returns the catalog entry called name (speech models and the VAD model).
func FindModel(name string) (Model, bool) {
	for _, m := range Models {
		if m.Name == name {
			return m, true
		}
	}
	if name == VADModel.Name {
		return VADModel, true
	}
	return Model{}, false
}

// url of the model file on base (https://huggingface.co).
func (m Model) url(base string) string {
	return strings.TrimRight(base, "/") + "/" + m.Repo + "/resolve/" + m.Revision + "/" + m.File
}
