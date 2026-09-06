package config

import "testing"

func TestLearningCodexCloudPolicy(t *testing.T) {
	for _, mode := range []string{"hybrid", "cloud_only"} {
		s := learningSettings()
		s.Mode, s.Skills.LocalOnly = mode, false
		s.Models[0].Model, s.Models[0].Locality = "gpt-5.6-sol", "cloud"
		s.Providers[0].Kind, s.Providers[0].Endpoint, s.Providers[0].Executable = "codex_app_server", "", "/bin/codex"
		if err := s.Validate(); err != nil {
			t.Fatal(mode, err)
		}
		for _, change := range []string{"local_mode", "local_scope", "local_model", "executable", "wrong_model"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				copy := s
				copy.Models = append([]Model(nil), s.Models...)
				copy.Providers = append([]Provider(nil), s.Providers...)
				switch change {
				case "local_mode":
					copy.Mode = "local_only"
				case "local_scope":
					copy.Skills.LocalOnly = true
				case "local_model":
					copy.Models[0].Locality = "local"
				case "executable":
					copy.Providers[0].Executable = ""
				case "wrong_model":
					copy.Models[0].Model = "some-other-model"
				}
				if copy.Validate() == nil {
					t.Fatal("unsafe native learning admitted")
				}
			})
		}
	}
}
