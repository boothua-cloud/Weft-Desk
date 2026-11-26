#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <dispatch/dispatch.h>
#include "appname_darwin.h"

void weft_set_process_name(const char *name) {
	@autoreleasepool {
		NSString *s = [NSString stringWithUTF8String:name];
		[[NSProcessInfo processInfo] setProcessName:s];
	}
}

// 菜单栏左上角显示的应用名就是 mainMenu 第一项的标题，读出来便于自检。
const char *weft_menu_bar_title(void) {
	NSMenu *m = [NSApp mainMenu];
	if (m == nil || m.numberOfItems == 0) {
		return NULL;
	}
	NSString *t = [[m itemAtIndex:0] title];
	return t == nil ? NULL : strdup([t UTF8String]);
}
