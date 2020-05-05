# goocp
基于gokcp(https://github.com/shaoyuan1943/gokcp)实现的类Reactor模式的简单UDP会话管理。

goocp的实现相当简洁，它其实是在探索如何以更简洁、简单的方式编写网络部分的代码。近些年，网络代码在结构上被划分成Reactor模式与非Reactor模式，无论是哪种方式，多线程的代码必然会复杂。

goocp基于Go的goroutines特性，以非常简单的方式实现了类Reactor模式，将网络细节部分隐藏在对应的goroutine中，同时尝试与应用层解耦，应用层以接口回调(handler)方式注入到goocp中，应用层不需要考虑数据何时抵达，也不需要考虑是否需要数据缓存，只要流速够快，应用层处理的足够及时。
