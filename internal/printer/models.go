package printer

import "strings"

// Camera protocols.
const (
	CameraRTSPS = "rtsps"    // rtsps://…:322/streaming/live/1 (X1 series, H2 series, …)
	CameraJPEG  = "jpeg6000" // proprietary JPEG stream on port 6000 (P1 / A1 series) — not supported yet
)

// Model describes a printer model.
type Model struct {
	Alias        string `json:"alias"`
	PrinterModel string `json:"printer_model"` // Bambu Studio printer_model (machine preset prefix)
	ModelID      string `json:"model_id"`      // SSDP DevModel / printers/<id>.json
	Camera       string `json:"camera"`
}

// Models is the model table (from Bambu Studio resources/printers/*.json).
var Models = []Model{
	{"X1C", "Bambu Lab X1 Carbon", "BL-P001", CameraRTSPS},
	{"X1", "Bambu Lab X1", "BL-P002", CameraRTSPS},
	{"X1E", "Bambu Lab X1E", "C13", CameraRTSPS},
	{"P1P", "Bambu Lab P1P", "C11", CameraJPEG},
	{"P1S", "Bambu Lab P1S", "C12", CameraJPEG},
	{"A1M", "Bambu Lab A1 mini", "N1", CameraJPEG},
	{"A1", "Bambu Lab A1", "N2S", CameraJPEG},
	{"P2S", "Bambu Lab P2S", "N7", CameraRTSPS},
	{"A2L", "Bambu Lab A2L", "N9", CameraJPEG},
	{"X2D", "Bambu Lab X2D", "N6", CameraRTSPS},
	{"H2D", "Bambu Lab H2D", "O1D", CameraRTSPS},
	{"H2DP", "Bambu Lab H2D Pro", "O1E", CameraRTSPS},
	{"H2S", "Bambu Lab H2S", "O1S", CameraRTSPS},
	{"H2C", "Bambu Lab H2C", "O1C", CameraRTSPS},
}

// LookupModel finds a model by alias ("X1C"), Bambu Studio name ("Bambu Lab X1 Carbon", "X1 Carbon") or model id ("BL-P001").
func LookupModel(s string) (Model, bool) {
	k := norm(s)
	for _, m := range Models {
		if k == norm(m.Alias) || k == norm(m.PrinterModel) || k == norm(strings.TrimPrefix(m.PrinterModel, "Bambu Lab ")) || k == norm(m.ModelID) {
			return m, true
		}
	}
	if k == "a1mini" {
		return LookupModel("A1M")
	}
	return Model{}, false
}

// MachinePreset is the Bambu Studio machine preset name for a model and nozzle ("Bambu Lab X1 Carbon 0.4 nozzle").
func (m Model) MachinePreset(nozzle string) string {
	return m.PrinterModel + " " + nozzle + " nozzle"
}

// Aliases lists model aliases for help text.
func Aliases() []string {
	out := make([]string, len(Models))
	for i, m := range Models {
		out[i] = m.Alias
	}
	return out
}

func norm(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.TrimSpace(s)))
}
