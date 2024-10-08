**OUTDATED!!!**

---

## Running node with the sequencer

**Sequencer** is the following:
1. a token holder who choose to run a sequencer program. Sequencer is active participant in the cooperative consensus    
2. a program, which performs sequencer chain building strategy on behalf of the token holder
3. a chain of transactions constantly produced by the sequencer program

In general, token holder can issue transaction any preferred way. 
For example, token holder can access the UTXO tangle and submit sequencer transactions to the network through API.

However, for performance and convenience reasons we developed a _sequencer program_ as part of the node. 
By the choice of the node owner, the sequencer can be started and run on the node by enabling it in the node's configuration. 
This way the whole node can be seen as an automated wallet, which periodically issues transactions to the network.

Each node can even be configured to run several sequencers, however running one sequencer is the most practical approach.

The following are step-by-step instructions how to initialize and start Proxima node with one sequencer on it.

### 1. Start the node as access node
Follow instructions provided in [Running access node](run_access.md).

After node is started and synced with the network, sequencer can be configured and started.

### 2. Create sequencer controller's wallet
Create `proxi` wallet profile as described in [CLI wallet program](proxi.md).
Do not change `wallet.sequencer_id` yet. The `tag_along.sequencer_id` may need adjustment to another preferred sequencer.

Now any other token holder can transfer `1.000.001.000.000` tokens to your account with the 
command `proxi node transfer 1000001000000 -t "addressED25519(<your address data>)"`. 

Let's assume we already have at least `1.000.001.000.000` tokens in your account at `addressED25519(<your address data>)`. 
This can be checked with the command `proxi node balance` run in the working directory which contain wallet profile.  

### 3. Create sequencer chain origin
Command `proxi node mkchain 1000000000000` will create output which is the sequencer chain origin, with `1000000000000` tokens on it.
`500` tokens will go as tag-along fee to the tag-along sequencer configured in the wallet. 
The remaining `999500` tokens will stay in the `addressED25519(<address data>)`.

You can check your balances with `proxi node balance`. It will show the balance of the chain output controlled
by the private key of the wallet.

You can list all chains controlled by your wallet by `proxi node chains`. There you can find respective chain IDs. 

### 4. Adjust wallet profile
Set `wallet.sequencer_id` to the `chain ID` of the newly created chain. 

This will enable the wallet to withdraw tokens from the sequencer with `proxi node seq withdraw` command.

### 5. Configure the sequencer
Sequencer is configured in the `sequencers` section of the `proxima.yaml`.
If template of the section wasn't generated by the `proxi init node -s` command, it must be added manually.

* Replace the placeholder `<local_seq_name>` with your given name of the sequencer. Let's say it is `mySeq`
* Run the command `proxi node chains` to display the `chain ID` of the chain origin created in the previous step.
* Copy the `chain ID` of the newly created chain from the terminal output of the previous command
* Replace the placeholder `<sequencer ID hex encoded>` in the  YAML key `sequncers.mySeq.sequencer_id` with the chain 
ID copied in the previous step
* Put private key of your wallet into the YAML key `sequencers.mySeq.controller_key`
* Put value `true` into the key `sequencers.mySeq.enable`

### 6. Run node with the sequencer
Stop the node with `ctrl-C` if necessary. Then start it again. Sequencer will start automatically after approx 10 sec. 

Check the logs to make sure everything is ok.
