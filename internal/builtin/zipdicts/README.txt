Bundled ZipCracker CTF dictionary
=================================

password_list.txt is the public default wordlist shipped by Hx0 ZipCracker
(https://github.com/asaotomo/ZipCracker). It contains about 6000 common
weak passwords used in CTF ZIP tasks.

DeepSentry embeds this file and treats the aliases builtin, 6000, 6000.txt,
password_list.txt, and zipcracker as the same built-in dictionary. The Go
recovery pipeline does not copy ZipCracker's Python source.
