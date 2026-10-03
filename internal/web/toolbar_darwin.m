// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

#import <Cocoa/Cocoa.h>
#include <stdint.h>
extern void goWebToolbarAction(uintptr_t,int);

@interface KWToolbarView : NSView
@property(nonatomic,assign) id owner;
@end
@interface KWWebToolbar : NSObject {
@public
 NSWindow *window;
 NSView *browser,*root;
 KWToolbarView *bar;
 NSButton *handle;
 NSTextField *identity;
 NSMutableArray *buttons;
 NSTimer *timer;
 id keyMonitor;
 NSString *details;
 NSString *pin,*unpin,*fullscreen,*windowed;
 uintptr_t token;
 BOOL pinned,dragging,closed;
 CGFloat offset,dragX;
 NSTimeInterval until;
}
- (void)layout;
- (void)reveal;
- (void)action:(NSButton*)sender;
- (void)tick:(NSTimer*)sender;
- (void)changed:(NSNotification*)notification;
@end
@implementation KWToolbarView
- (void)mouseDown:(NSEvent*)event { KWWebToolbar*t=self.owner;if((t->window.styleMask&NSWindowStyleMaskFullScreen)&&[self convertPoint:event.locationInWindow fromView:nil].x<22){t->dragging=YES;t->dragX=event.locationInWindow.x;} }
- (void)mouseDragged:(NSEvent*)event { KWWebToolbar*t=self.owner;if(t->dragging){t->offset+=event.locationInWindow.x-t->dragX;t->dragX=event.locationInWindow.x;[t layout];[t reveal];} }
- (void)mouseUp:(NSEvent*)event { (void)event;((KWWebToolbar*)self.owner)->dragging=NO; }
@end
@implementation KWWebToolbar
- (void)layout {
 CGFloat w=root.bounds.size.width,h=root.bounds.size.height,bh=44;
 BOOL full=(window.styleMask&NSWindowStyleMaskFullScreen)!=0;
 browser.frame=NSMakeRect(0,0,w,full?h:MAX(1,h-bh));
 CGFloat bw=full?MIN(w,1040):w,left=full?MAX(0,MIN((w-bw)/2+offset,w-bw)):0;
 bar.frame=NSMakeRect(left,h-bh,bw,bh);
 handle.frame=NSMakeRect((w-100)/2,h-22,100,22);
 CGFloat x=bw-8;
 for(NSButton*b in [buttons reverseObjectEnumerator]) {CGFloat width=MAX(65,b.intrinsicContentSize.width+12);x-=width;b.frame=NSMakeRect(x,8,width,28);x-=6;}
 identity.frame=NSMakeRect(24,10,MAX(0,x-28),24);
}
- (void)reveal {until=[NSDate timeIntervalSinceReferenceDate]+3;bar.hidden=NO;handle.hidden=YES;}
- (void)action:(NSButton*)sender {
 int a=(int)sender.tag;
 if(a==1){BOOL entering=!(window.styleMask&NSWindowStyleMaskFullScreen);[window toggleFullScreen:self];
  for(NSButton*b in buttons)if(b.tag==1)b.title=entering?windowed:fullscreen;
  [self reveal];}
 else if(a==4){NSAlert*d=[[[NSAlert alloc]init]autorelease];d.messageText=@"Connection";d.informativeText=details;[d runModal];}
 else if(a==5){pinned=!pinned;sender.title=pinned?unpin:pin;[self reveal];}
 else if(a==7){[self reveal];}
 else goWebToolbarAction(token,a);
}
- (void)tick:(NSTimer*)sender {
 (void)sender;if(closed)return;
 BOOL full=(window.styleMask&NSWindowStyleMaskFullScreen)!=0;
 NSPoint p=[root convertPoint:[window mouseLocationOutsideOfEventStream] fromView:nil];
 BOOL hover=NSPointInRect(p,bar.frame)||NSPointInRect(p,handle.frame);
 NSResponder*f=window.firstResponder;BOOL focus=[f isKindOfClass:[NSView class]]&&[(NSView*)f isDescendantOf:bar];
 if(hover){[self reveal];return;}
 if(full&&!pinned&&!dragging&&!focus&&[NSDate timeIntervalSinceReferenceDate]>until){bar.hidden=YES;handle.hidden=NO;}
}
- (void)changed:(NSNotification*)notification {
 if([notification.name isEqual:NSWindowWillCloseNotification]){closed=YES;return;}
 [self layout];
 // A resize is not something the user asked the chrome for: only entering or
 // leaving fullscreen brings the strip back, the way kw_size lays out without
 // revealing and kw_state does both.
 if([notification.name isEqual:NSWindowDidEnterFullScreenNotification]||[notification.name isEqual:NSWindowDidExitFullScreenNotification])[self reveal];
}
@end

void *kw_cocoa_attach(void *win,uintptr_t token,const char *labels,const char *info){
 NSWindow*w=(NSWindow*)win;if(!w)return NULL;
 KWWebToolbar*t=[[KWWebToolbar alloc]init];t->window=[w retain];t->token=token;t->details=[[NSString stringWithUTF8String:info]copy];
 t->browser=[w.contentView retain];t->root=[[NSView alloc]initWithFrame:t->browser.frame];t->root.autoresizingMask=NSViewWidthSizable|NSViewHeightSizable;
 [t->browser removeFromSuperview];[t->root addSubview:t->browser];w.contentView=t->root;w.contentMinSize=NSMakeSize(700,180);
 t->bar=[[KWToolbarView alloc]initWithFrame:NSZeroRect];t->bar.owner=t;t->bar.wantsLayer=YES;t->bar.layer.backgroundColor=NSColor.windowBackgroundColor.CGColor;[t->root addSubview:t->bar];
 NSArray*parts=[[NSString stringWithUTF8String:labels]componentsSeparatedByString:@"\n"];
 // The alternate titles the chrome needs once it has relabelled a button in
 // place, so it stays translatable like the shell's. The fallbacks only matter
 // if a caller sends a short label list.
 NSString *pinText=parts.count>5&&[parts[5] length]?parts[5]:@"Pin";
 NSString *unpinText=parts.count>8&&[parts[8] length]?parts[8]:@"Unpin";
 NSString *fullText=parts.count>1&&[parts[1] length]?parts[1]:@"Fullscreen";
 NSString *windowedText=parts.count>9&&[parts[9] length]?parts[9]:@"Windowed";
 NSString *toolsText=parts.count>7&&[parts[7] length]?parts[7]:@"Tools";
 t->pin=[pinText copy];t->unpin=[unpinText copy];t->fullscreen=[fullText copy];t->windowed=[windowedText copy];
 t->identity=[[NSTextField labelWithString:parts[0]]retain];t->identity.lineBreakMode=NSLineBreakByTruncatingTail;[t->bar addSubview:t->identity];t->buttons=[[NSMutableArray alloc]init];
 for(int a=1;a<=6;a++){if(a>=parts.count||[parts[a]length]==0)continue;NSButton*b=[NSButton buttonWithTitle:parts[a] target:t action:@selector(action:)];b.tag=a;b.bezelStyle=NSBezelStyleRounded;[t->bar addSubview:b];[t->buttons addObject:b];}
 t->handle=[[NSButton buttonWithTitle:toolsText target:t action:@selector(action:)]retain];t->handle.tag=7;[t->root addSubview:t->handle];
 for(NSString*n in @[NSWindowDidResizeNotification,NSWindowDidEnterFullScreenNotification,NSWindowDidExitFullScreenNotification,NSWindowWillCloseNotification])[[NSNotificationCenter defaultCenter]addObserver:t selector:@selector(changed:) name:n object:w];
 t->keyMonitor=[[NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown handler:^NSEvent*(NSEvent*e){if(e.window!=w)return e;NSString*s=e.charactersIgnoringModifiers;NSUInteger mods=e.modifierFlags;
 if(e.keyCode==103){for(NSButton*b in t->buttons)if(b.tag==1){[t action:b];break;}return nil;}
 if((mods&(NSEventModifierFlagControl|NSEventModifierFlagOption))==(NSEventModifierFlagControl|NSEventModifierFlagOption)&&[s.lowercaseString isEqual:@"q"]){goWebToolbarAction(t->token,6);return nil;}
 if((mods&(NSEventModifierFlagControl|NSEventModifierFlagOption|NSEventModifierFlagShift))==(NSEventModifierFlagControl|NSEventModifierFlagOption|NSEventModifierFlagShift)&&[s.lowercaseString isEqual:@"t"]){[t reveal];[w makeFirstResponder:t->buttons.firstObject];return nil;}return e;}]retain];
 t->timer=[[NSTimer scheduledTimerWithTimeInterval:.1 target:t selector:@selector(tick:) userInfo:nil repeats:YES]retain];[t layout];[t reveal];return t;
}
void kw_cocoa_detach(void *ptr){KWWebToolbar*t=ptr;if(!t)return;[t->timer invalidate];[t->timer release];[NSEvent removeMonitor:t->keyMonitor];[t->keyMonitor release];[[NSNotificationCenter defaultCenter]removeObserver:t];[t->buttons release];[t->details release];[t->pin release];[t->unpin release];[t->fullscreen release];[t->windowed release];[t->handle release];[t->identity release];[t->bar release];[t->browser release];[t->root release];[t->window release];[t release];}
