#!/bin/sh

# Update desktop database for .desktop file changes
# This makes the application appear in application menus and registers its capabilities.
if command -v update-desktop-database >/dev/null 2>&1; then
  echo "Updating desktop database..."
  update-desktop-database -q /usr/share/applications
else
  echo "Warning: update-desktop-database command not found. Desktop file may not be immediately recognized." >&2
fi

# 图标缓存不重建的话，新装的图标在菜单和 Dock 里要等重新登录才可能出现。
# 精简系统可能没装 gtk-update-icon-cache，缺了不影响 dpkg 收尾。
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor 2>/dev/null || true
else
  echo "Warning: gtk-update-icon-cache not found. Icon may not show until next login." >&2
fi

# Update MIME database for custom URL schemes (x-scheme-handler)
# This ensures the system knows how to handle your custom protocols.
if command -v update-mime-database >/dev/null 2>&1; then
  echo "Updating MIME database..."
  update-mime-database -n /usr/share/mime
else
  echo "Warning: update-mime-database command not found. Custom URL schemes may not be immediately recognized." >&2
fi

exit 0
