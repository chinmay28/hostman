# icongen — every icon from the one logo

[`art/hostman-logo.png`](../../art/hostman-logo.png) is the one drawing of
HostMan. Every icon a platform wants is cut from it by `make icons` (this
module, its own so the server carries no image dependency), and `make
icons-check` — also `go test ./...` here, which CI runs — fails if a committed
copy has drifted from what the logo makes. Never edit a derived file by hand:
the next `make icons` puts it back.

## What gets made

| Where | What | Shape |
|-------|------|-------|
| `apps/web/public/` | `icon-192.png` (header mark, favicon), `icon-512.png` | rounded tile, on transparency |
| | `apple-touch-icon.png` | full bleed: iOS cuts its own corners |
| | `icon-512-maskable.png` | cropped wider, so a circular mask keeps the face |
| | `logo.png` | the whole drawing on transparency, for the double-tap flash |
| `apps/android/app/src/main/res/` | `mipmap-*/ic_launcher.png` | rounded tile, 48dp in every density |
| | `mipmap-*/ic_launcher_foreground.png`, `mipmap-anydpi-v26/ic_launcher.xml`, `values/ic_launcher_background.xml` | adaptive icon: the maskable crop in the 72dp a launcher shows, over the badge colour |
| | `drawable-nodpi/hostman_logo.png` | the mark on the app's own screens (setup, status) |

Two Android icons are not generated, because Android draws them as a
single-colour mask and the artwork would show as a blank square:
`drawable/ic_stat_hostman.xml` (the notification icon) and
`drawable/ic_launcher_monochrome.xml` (Android 13's themed launcher icon, which
`ic_launcher.xml` names). Both are the same silhouette — two stacked servers
on a stand, HostMan's racks — drawn by hand on the 24dp grid. If the artwork
changes shape, redraw them to match.

## What it assumes about the logo

The badge colour (`badge`) and the point the icons centre on (`focus`, the
mascot's chest) are written down in `main.go`, measured off this drawing. The
white page around the badge is painted over before cropping. A new logo with a
different badge or composition needs both measured again.
