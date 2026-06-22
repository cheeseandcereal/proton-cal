package cli

import "testing"

func TestCreateRemindersResolver(t *testing.T) {
	t.Run("none flags -> inherit", func(t *testing.T) {
		f := reminderColorFlags{}
		list, set, err := f.createReminders()
		if err != nil || set || list != nil {
			t.Errorf("got (%v, %v, %v), want (nil,false,nil)", list, set, err)
		}
	})
	t.Run("no-reminders -> explicit none", func(t *testing.T) {
		f := reminderColorFlags{noReminders: true}
		list, set, err := f.createReminders()
		if err != nil || !set || len(list) != 0 {
			t.Errorf("got (%v, %v, %v), want ([],true,nil)", list, set, err)
		}
	})
	t.Run("custom list", func(t *testing.T) {
		f := reminderColorFlags{reminder: []string{"15m", "email:1h"}}
		list, set, err := f.createReminders()
		if err != nil || !set || len(list) != 2 {
			t.Fatalf("got (%v, %v, %v)", list, set, err)
		}
		if list[0].Trigger != "-PT15M" || list[1].Type != 0 {
			t.Errorf("parsed = %+v", list)
		}
	})
}

func TestUpdateRemindersResolver(t *testing.T) {
	t.Run("none -> keep (nil)", func(t *testing.T) {
		f := reminderColorFlags{}
		u, err := f.updateReminders()
		if err != nil || u != nil {
			t.Errorf("got (%v, %v), want (nil,nil)", u, err)
		}
	})
	t.Run("reminders-default -> inherit", func(t *testing.T) {
		f := reminderColorFlags{remindersDefault: true}
		u, err := f.updateReminders()
		if err != nil || u == nil || !u.Inherit {
			t.Errorf("got (%+v, %v), want inherit", u, err)
		}
	})
	t.Run("no-reminders -> explicit none", func(t *testing.T) {
		f := reminderColorFlags{noReminders: true}
		u, err := f.updateReminders()
		if err != nil || u == nil || u.Inherit || len(u.List) != 0 {
			t.Errorf("got (%+v, %v), want none", u, err)
		}
	})
	t.Run("custom", func(t *testing.T) {
		f := reminderColorFlags{reminder: []string{"30m"}}
		u, err := f.updateReminders()
		if err != nil || u == nil || u.Inherit || len(u.List) != 1 || u.List[0].Trigger != "-PT30M" {
			t.Errorf("got (%+v, %v)", u, err)
		}
	})
}

func TestCreateColorResolver(t *testing.T) {
	t.Run("empty -> inherit", func(t *testing.T) {
		f := reminderColorFlags{}
		got, err := f.createColor()
		if err != nil || got != "" {
			t.Errorf("got (%q, %v), want (empty, nil)", got, err)
		}
	})
	t.Run("default -> inherit", func(t *testing.T) {
		f := reminderColorFlags{color: "default"}
		got, err := f.createColor()
		if err != nil || got != "" {
			t.Errorf("got (%q, %v), want (empty, nil)", got, err)
		}
	})
	t.Run("name -> hex", func(t *testing.T) {
		f := reminderColorFlags{color: "strawberry"}
		got, err := f.createColor()
		if err != nil || got != "#EC3E7C" {
			t.Errorf("got (%q, %v)", got, err)
		}
	})
}
