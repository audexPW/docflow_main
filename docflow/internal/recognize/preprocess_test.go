package recognize

import "testing"

// Разбор ответа Tesseract OSD. Формат стабильный, но важно не перепутать знак
// и не поворачивать страницу при низкой уверенности: ложный разворот ровного
// листа хуже, чем оставленный боком.
func TestParseOSDOutput(t *testing.T) {
	cases := []struct {
		name string
		out  string
		conf float64
		want int
	}{
		{
			name: "лист повёрнут на 90 против часовой",
			out:  "Page number: 0\nOrientation in degrees: 270\nRotate: 90\nOrientation confidence: 4.61\nScript: Cyrillic\n",
			want: 90,
		},
		{
			name: "вверх ногами",
			out:  "Rotate: 180\nOrientation confidence: 12.30\n",
			want: 180,
		},
		{
			name: "ровная страница",
			out:  "Rotate: 0\nOrientation confidence: 8.10\n",
			want: 0,
		},
		{
			name: "уверенности не хватает — не трогаем",
			out:  "Rotate: 90\nOrientation confidence: 0.35\n",
			want: 0,
		},
		{
			name: "мусор вместо ответа",
			out:  "Too few characters. Skipping this page\n",
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseOSD(c.out, 1.0); got != c.want {
				t.Errorf("получено %d, ожидалось %d", got, c.want)
			}
		})
	}
}

// Угол поворота для ImageMagick — обратный тому, что сообщает OSD.
func TestRotateArgIsInverse(t *testing.T) {
	for deg, want := range map[int]int{90: 270, 180: 180, 270: 90} {
		if got := 360 - deg; got != want {
			t.Errorf("OSD %d° → magick %d°, ожидалось %d°", deg, got, want)
		}
	}
}
