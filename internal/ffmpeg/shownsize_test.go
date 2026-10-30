package ffmpeg

import "testing"

func TestDecodeShownSizeTurnsQuarterTurns(t *testing.T) {
	cases := map[string][2]int{
		`{"frames":[{"width":64,"height":32}]}`:                                        {64, 32},
		`{"frames":[{"width":64,"height":32,"side_data_list":[{"rotation":-90},{}]}]}`: {32, 64},
		`{"frames":[{"width":64,"height":32,"side_data_list":[{"rotation":90}]}]}`:     {32, 64},
		`{"frames":[{"width":64,"height":32,"side_data_list":[{"rotation":270}]}]}`:    {32, 64},
		`{"frames":[{"width":64,"height":32,"side_data_list":[{"rotation":180}]}]}`:    {64, 32},
		`{"frames":[{"width":64,"height":32,"side_data_list":[{}]}]}`:                  {64, 32},
	}
	for in, want := range cases {
		w, h, err := decodeShownSize([]byte(in))
		if err != nil || w != want[0] || h != want[1] {
			t.Errorf("%s: %d×%d %v, want %d×%d", in, w, h, err, want[0], want[1])
		}
	}
}

func TestDecodeShownSizeRefusesNoPicture(t *testing.T) {
	for _, in := range []string{`{"frames":[]}`, `{"frames":[{"width":0,"height":0}]}`, `not json`} {
		if _, _, err := decodeShownSize([]byte(in)); err == nil {
			t.Errorf("%s: accepted", in)
		}
	}
}
