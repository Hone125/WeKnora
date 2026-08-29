// dataset 生成器：把内置的中文科普 QA 评测集写成 parquet 文件。
//
// 用法（在仓库根目录执行）：
//
//	go run ./dataset
//
// 会在 ./dataset/samples/ 下生成 queries/corpus/answers/qrels/qas 五个 parquet 文件，
// 覆盖现有 samples。字段与 internal/application/service/dataset.go 的读取端严格一致：
//
//	queries.parquet : id(int64), text(string)
//	corpus.parquet  : id(int64), text(string)
//	answers.parquet : id(int64), text(string)
//	qrels.parquet   : qid(int64), pid(int64)
//	qas.parquet     : qid(int64), aid(int64)
//
// 数据规模：6 个主题 × 5 段话 = 30 段语料（corpus），30 个问题（query），
// 每个问题对应本主题 1 个相关段落（qrels 一对一），每个问题 1 个标准答案（qas）。
// 同一主题内其余 4 段话是"强干扰"，其它主题 25 段是"弱干扰"，使检索指标具备区分度。
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/parquet-go/parquet-go"
)

// TextInfo 与 dataset.go 读取端结构一致（id/text）。
type TextInfo struct {
	ID   int64  `parquet:"id"`
	Text string `parquet:"text"`
}

// RelsInfo 记录 question -> passage 的相关性标注。
type RelsInfo struct {
	QID int64 `parquet:"qid"`
	PID int64 `parquet:"pid"`
}

// QaInfo 记录 question -> answer 的对应关系。
type QaInfo struct {
	QID int64 `parquet:"qid"`
	AID int64 `parquet:"aid"`
}

// qaItem 是数据源里一个完整的问题条目：问题文本 + 唯一相关段落 + 标准答案。
// 生成时把 PID 展开成 qrels 一行，把 Answer 展开成 answers 一行 + qas 一行。
type qaItem struct {
	id       int64
	question string
	pid      int64
	answer   string
}

// passages 是 30 段中文科普语料，按 6 个主题各 5 段组织（pid 1~30）。
var passages = []TextInfo{
	// 主题 1：操作系统（pid 1~5）
	{ID: 1, Text: "操作系统是管理计算机硬件与软件资源、并为应用程序提供通用服务的系统软件。它是用户与计算机硬件之间的桥梁，核心功能包括进程调度、内存管理、文件管理和设备管理。"},
	{ID: 2, Text: "进程是程序在操作系统中的一次执行实例，拥有独立的地址空间和系统资源；线程是进程内部的执行单元，共享进程的资源。多线程可以在一个进程内并发执行多个任务。"},
	{ID: 3, Text: "操作系统通过内存管理单元（MMU）实现虚拟内存，让每个进程拥有独立的逻辑地址空间。分页和分段是两种主要的内存管理方式，页面置换算法（如 LRU）用于决定换出哪些页面。"},
	{ID: 4, Text: "文件系统负责在存储设备上组织和管理数据，提供文件的创建、读取、写入、删除和目录管理等功能。常见的文件系统包括 Windows 的 NTFS、Linux 的 ext4 和 macOS 的 APFS。"},
	{ID: 5, Text: "主流的桌面操作系统包括微软的 Windows、苹果的 macOS 以及各种 Linux 发行版（如 Ubuntu）。移动端操作系统则以 Android 和 iOS 为主。"},

	// 主题 2：计算机网络（pid 6~10）
	{ID: 6, Text: "OSI 参考模型把网络通信自下而上分为七层：物理层、数据链路层、网络层、传输层、会话层、表示层和应用层，每一层为上一层提供服务。"},
	{ID: 7, Text: "TCP/IP 是互联网的基础协议栈，核心包括网络层的 IP 协议（负责寻址和路由）以及传输层的 TCP 和 UDP。TCP 面向连接、保证可靠传输，UDP 无连接、传输快但不保证可靠。"},
	{ID: 8, Text: "IP 地址是网络设备的逻辑地址，IPv4 为 32 位，IPv6 为 128 位。DNS（域名系统）把人类易记的域名（如 www.example.com）解析为对应的 IP 地址。"},
	{ID: 9, Text: "HTTP 是应用层协议，用于在浏览器和服务器之间传输超文本。HTTPS 在 HTTP 的基础上加入 TLS/SSL 加密，保证数据传输的机密性和完整性。"},
	{ID: 10, Text: "局域网（LAN）覆盖范围较小，如家庭或办公室；广域网（WAN）覆盖范围较大，如城市或国家。路由器负责在不同网络之间转发数据。"},

	// 主题 3：数据结构与算法（pid 11~15）
	{ID: 11, Text: "数组是连续内存存储的线性结构，支持按下标随机访问，但插入和删除开销较大；链表由节点和指针组成，插入删除方便，但无法直接随机访问。"},
	{ID: 12, Text: "栈是后进先出（LIFO）的结构，只在一端进行插入和删除；队列是先进先出（FIFO）的结构，一端入队、另一端出队。"},
	{ID: 13, Text: "树是层次化的数据结构，二叉树每个节点最多有两个子节点。二叉搜索树满足左子树节点值小于根、右子树节点值大于根，支持高效的查找。"},
	{ID: 14, Text: "常见的排序算法包括冒泡排序、快速排序和归并排序等。快速排序的平均时间复杂度为 O(n log n)，最坏情况为 O(n^2)。"},
	{ID: 15, Text: "哈希表通过哈希函数把键映射到数组下标，平均查找时间复杂度为 O(1)。哈希冲突通过链地址法或开放寻址法解决。"},

	// 主题 4：数据库（pid 16~20）
	{ID: 16, Text: "关系型数据库用表（关系）来存储数据，通过主键和外键建立表之间的联系。常见的关系型数据库包括 MySQL 和 PostgreSQL。"},
	{ID: 17, Text: "SQL 是操作关系型数据库的标准语言，包括 DDL（数据定义）和 DML（数据操作）。SELECT 用于查询，INSERT、UPDATE、DELETE 分别用于增、改、删。"},
	{ID: 18, Text: "事务是数据库操作的最小逻辑单元，具有 ACID 四个特性：原子性（Atomicity）、一致性（Consistency）、隔离性（Isolation）和持久性（Durability）。"},
	{ID: 19, Text: "索引是加速数据库查询的数据结构，常见的有 B+ 树索引。合理建立索引能显著提升查询速度，但会带来额外的写入开销。"},
	{ID: 20, Text: "NoSQL 泛指非关系型数据库，常见类型包括键值存储（如 Redis）、文档数据库（如 MongoDB）和列族存储，适合高并发和大数据场景。"},

	// 主题 5：人工智能（pid 21~25）
	{ID: 21, Text: "机器学习让计算机从数据中学习规律，主要分为监督学习、无监督学习和强化学习。监督学习使用带标签的数据进行训练。"},
	{ID: 22, Text: "深度学习使用多层神经网络自动提取数据特征，反向传播算法用于更新网络权重。卷积神经网络（CNN）擅长处理图像数据。"},
	{ID: 23, Text: "自然语言处理（NLP）让计算机理解和生成人类语言，常见任务包括分词、命名实体识别、机器翻译和情感分析等。"},
	{ID: 24, Text: "计算机视觉让机器理解和分析图像与视频，常见任务包括图像分类、目标检测和图像分割，卷积神经网络是常用模型。"},
	{ID: 25, Text: "大语言模型（LLM）是基于海量文本训练的深度学习模型，能完成问答、翻译和摘要等任务，Transformer 是其核心架构。"},

	// 主题 6：计算机硬件（pid 26~30）
	{ID: 26, Text: "CPU（中央处理器）是计算机的运算和控制核心，负责执行指令。主要性能指标包括主频、核心数和缓存。"},
	{ID: 27, Text: "RAM（随机存取存储器）是计算机的主存，用于临时存放正在运行的程序和数据，断电后其中的数据会丢失。"},
	{ID: 28, Text: "机械硬盘（HDD）用磁盘存储数据，容量大、价格低但速度慢；固态硬盘（SSD）用闪存存储，读写速度快但价格较高。"},
	{ID: 29, Text: "GPU（图形处理器）擅长大规模并行计算，最初用于图形渲染，现在也广泛用于深度学习和科学计算。"},
	{ID: 30, Text: "主板负责连接 CPU、内存、硬盘等各个部件，总线则在各部件之间负责传输数据。"},
}

// qaItems 是 30 个问题，qid 1~30，每个问题对应一个相关段落（pid）和一条标准答案。
var qaItems = []qaItem{
	// 主题 1：操作系统
	{id: 1, question: "操作系统有哪些核心功能？", pid: 1, answer: "操作系统的核心功能包括进程调度、内存管理、文件管理和设备管理。"},
	{id: 2, question: "进程和线程有什么区别？", pid: 2, answer: "进程是程序的一次执行实例，拥有独立地址空间；线程是进程内的执行单元，共享进程资源。"},
	{id: 3, question: "虚拟内存是通过什么机制实现的？", pid: 3, answer: "虚拟内存通过内存管理单元（MMU）实现，分页和分段是两种主要的内存管理方式。"},
	{id: 4, question: "常见的文件系统有哪些？", pid: 4, answer: "常见的文件系统包括 Windows 的 NTFS、Linux 的 ext4 和 macOS 的 APFS。"},
	{id: 5, question: "主流的桌面操作系统有哪些？", pid: 5, answer: "主流的桌面操作系统包括 Windows、macOS 以及 Ubuntu 等 Linux 发行版。"},

	// 主题 2：计算机网络
	{id: 6, question: "OSI 参考模型分为哪几层？", pid: 6, answer: "OSI 模型分为物理层、数据链路层、网络层、传输层、会话层、表示层和应用层七层。"},
	{id: 7, question: "TCP 和 UDP 的主要区别是什么？", pid: 7, answer: "TCP 面向连接、保证可靠传输；UDP 无连接、传输快但不保证可靠。"},
	{id: 8, question: "DNS 的作用是什么？", pid: 8, answer: "DNS 把人类易记的域名解析为对应的 IP 地址。"},
	{id: 9, question: "HTTPS 相比 HTTP 增加了什么？", pid: 9, answer: "HTTPS 在 HTTP 基础上加入 TLS/SSL 加密，保证数据传输的机密性和完整性。"},
	{id: 10, question: "局域网和广域网有什么区别？", pid: 10, answer: "局域网覆盖范围小（如家庭、办公室），广域网覆盖范围大（如城市、国家）。"},

	// 主题 3：数据结构与算法
	{id: 11, question: "数组和链表有什么区别？", pid: 11, answer: "数组连续存储、支持随机访问但插入删除开销大；链表插入删除方便但无法直接随机访问。"},
	{id: 12, question: "栈和队列的进出顺序有什么不同？", pid: 12, answer: "栈是后进先出（LIFO），队列是先进先出（FIFO）。"},
	{id: 13, question: "二叉搜索树有什么特点？", pid: 13, answer: "二叉搜索树左子树节点值小于根、右子树节点值大于根，支持高效查找。"},
	{id: 14, question: "快速排序的平均时间复杂度是多少？", pid: 14, answer: "快速排序的平均时间复杂度为 O(n log n)。"},
	{id: 15, question: "哈希表为什么平均查找很快？", pid: 15, answer: "哈希表通过哈希函数把键映射到数组下标，平均查找时间复杂度为 O(1)。"},

	// 主题 4：数据库
	{id: 16, question: "关系型数据库是怎么组织数据的？", pid: 16, answer: "关系型数据库用表存储数据，通过主键和外键建立表之间的联系。"},
	{id: 17, question: "SQL 中的 DML 操作有哪些？", pid: 17, answer: "SQL 的 DML 操作包括 SELECT 查询，以及 INSERT、UPDATE、DELETE 增改删。"},
	{id: 18, question: "事务的 ACID 特性指什么？", pid: 18, answer: "ACID 指原子性、一致性、隔离性和持久性。"},
	{id: 19, question: "索引的作用和代价是什么？", pid: 19, answer: "索引能加速查询（常见 B+ 树），但会带来额外的写入开销。"},
	{id: 20, question: "NoSQL 数据库有哪些常见类型？", pid: 20, answer: "NoSQL 常见类型包括键值存储、文档数据库和列族存储。"},

	// 主题 5：人工智能
	{id: 21, question: "机器学习主要分为哪几类？", pid: 21, answer: "机器学习主要分为监督学习、无监督学习和强化学习。"},
	{id: 22, question: "反向传播算法的作用是什么？", pid: 22, answer: "反向传播算法用于更新神经网络的权重。"},
	{id: 23, question: "自然语言处理有哪些常见任务？", pid: 23, answer: "自然语言处理的常见任务包括分词、命名实体识别、机器翻译和情感分析。"},
	{id: 24, question: "计算机视觉有哪些常见任务？", pid: 24, answer: "计算机视觉的常见任务包括图像分类、目标检测和图像分割。"},
	{id: 25, question: "大语言模型的核心架构是什么？", pid: 25, answer: "大语言模型的核心架构是 Transformer。"},

	// 主题 6：计算机硬件
	{id: 26, question: "CPU 的主要性能指标有哪些？", pid: 26, answer: "CPU 的主要性能指标包括主频、核心数和缓存。"},
	{id: 27, question: "RAM 断电后数据会怎样？", pid: 27, answer: "RAM 是易失性存储器，断电后其中的数据会丢失。"},
	{id: 28, question: "SSD 和机械硬盘有什么区别？", pid: 28, answer: "SSD 用闪存存储、速度快但价格高；机械硬盘容量大、价格低但速度慢。"},
	{id: 29, question: "GPU 为什么适合深度学习？", pid: 29, answer: "GPU 擅长大规模并行计算，因此适合深度学习和科学计算。"},
	{id: 30, question: "主板和总线的作用是什么？", pid: 30, answer: "主板连接 CPU、内存、硬盘等部件，总线负责在各部件之间传输数据。"},
}

func main() {
	outDir := "./dataset/samples"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("创建输出目录失败: %v", err)
	}

	// 由 qaItems 派生 queries / answers / qrels / qas
	queries := make([]TextInfo, 0, len(qaItems))
	answers := make([]TextInfo, 0, len(qaItems))
	qrels := make([]RelsInfo, 0, len(qaItems))
	qas := make([]QaInfo, 0, len(qaItems))
	for _, q := range qaItems {
		queries = append(queries, TextInfo{ID: q.id, Text: q.question})
		answers = append(answers, TextInfo{ID: q.id, Text: q.answer})
		qrels = append(qrels, RelsInfo{QID: q.id, PID: q.pid})
		qas = append(qas, QaInfo{QID: q.id, AID: q.id})
	}

	write := func(name string, writeFunc func(path string) error) {
		path := filepath.Join(outDir, name)
		if err := writeFunc(path); err != nil {
			log.Fatalf("写 %s 失败: %v", name, err)
		}
		log.Printf("已生成 %s", path)
	}

	write("queries.parquet", func(p string) error { return parquet.WriteFile(p, queries) })
	write("corpus.parquet", func(p string) error { return parquet.WriteFile(p, passages) })
	write("answers.parquet", func(p string) error { return parquet.WriteFile(p, answers) })
	write("qrels.parquet", func(p string) error { return parquet.WriteFile(p, qrels) })
	write("qas.parquet", func(p string) error { return parquet.WriteFile(p, qas) })

	log.Printf("完成：%d 个问题，%d 段语料，%d 条答案，%d 条相关性，%d 条问答映射",
		len(queries), len(passages), len(answers), len(qrels), len(qas))
}
