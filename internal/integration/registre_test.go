package integration

import "testing"

func TestRegistre(t *testing.T) {
	var r Registre
	if err := r.Declarer(Declaration{Nom: "sms", Mode: "fake", Variable: "DIGUEE_SMS"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Declarer(Declaration{Nom: "empreinteur", Mode: "ovh", Variable: "DIGUEE_EMPREINTEUR"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Declarer(Declaration{Nom: "sms", Mode: "orange", Variable: "DIGUEE_SMS"}); err == nil {
		t.Error("une double déclaration devrait être refusée")
	}
	if err := r.Declarer(Declaration{Nom: "x"}); err == nil {
		t.Error("une déclaration incomplète devrait être refusée")
	}
	ds := r.Declarations()
	if len(ds) != 2 || ds[0].Nom != "empreinteur" {
		t.Errorf("déclarations : %+v", ds)
	}
	fs := r.Fausses()
	if len(fs) != 1 || fs[0].Nom != "sms" {
		t.Errorf("fausses : %+v", fs)
	}
}
