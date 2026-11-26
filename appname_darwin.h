#ifndef WEFT_APPNAME_DARWIN_H
#define WEFT_APPNAME_DARWIN_H

// 修改进程名，让菜单栏与 Dock 显示中文应用名。
void weft_set_process_name(const char *name);

// 返回菜单栏应用名（mainMenu 第一项标题），失败返回 NULL。
const char *weft_menu_bar_title(void);

#endif
